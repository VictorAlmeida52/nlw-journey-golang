package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/discord-gophers/goapi-gen/types"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"journey/internal/api/spec"
	"journey/internal/pgstore"
	"net/http"
	"time"
)

type store interface {
	ConfirmTrip(context.Context, uuid.UUID) error
	CreateTrip(context.Context, *pgxpool.Pool, spec.CreateTripRequest) (uuid.UUID, error)
	GetTrip(context.Context, uuid.UUID) (pgstore.Trip, error)
	UpdateTrip(context.Context, pgstore.UpdateTripParams) error

	ConfirmParticipant(context.Context, uuid.UUID) error
	InviteParticipantToTrip(context.Context, pgstore.InviteParticipantToTripParams) (uuid.UUID, error)
	GetParticipants(context.Context, uuid.UUID) ([]pgstore.Participant, error)

	CreateActivity(context.Context, pgstore.CreateActivityParams) (uuid.UUID, error)
	GetTripActivities(ctx context.Context, tripID uuid.UUID) ([]pgstore.Activity, error)

	CreateTripLink(context.Context, pgstore.CreateTripLinkParams) (uuid.UUID, error)
	GetTripLinks(context.Context, uuid.UUID) ([]pgstore.Link, error)

	GetParticipant(context.Context, uuid.UUID) (pgstore.Participant, error)
}

type mailer interface {
	SendConfirmTripEmailToTripOwner(uuid.UUID) error
}

type API struct {
	store     store
	logger    *zap.Logger
	validator *validator.Validate
	pool      *pgxpool.Pool
	mailer    mailer
}

func NewApi(pool *pgxpool.Pool, logger *zap.Logger, mailer mailer) API {
	apiValidator := validator.New(validator.WithRequiredStructEnabled())
	return API{
		store:     pgstore.New(pool),
		logger:    logger,
		validator: apiValidator,
		pool:      pool,
		mailer:    mailer,
	}
}

// PatchParticipantsParticipantIDConfirm Confirms a participant on a trip.
// (PATCH /participants/{participantId}/confirm)
func (api API) PatchParticipantsParticipantIDConfirm(w http.ResponseWriter, r *http.Request, participantID string) *spec.Response {
	participantUUID, err := uuid.Parse(participantID)
	if err != nil {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "uuid inválido"})
	}

	participant, err := api.store.GetParticipant(r.Context(), participantUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "participante não encontrado"})
		}
		api.logger.Error("failed to get participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "something went wrong, try again"})
	}

	if participant.IsConfirmed {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "participante já confirmado"})
	}

	if err := api.store.ConfirmParticipant(r.Context(), participantUUID); err != nil {
		api.logger.Error("failed to confirm participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "something went wrong, try again"})
	}

	return spec.PatchParticipantsParticipantIDConfirmJSON204Response(nil)
}

// PostTrips Create a new trip
// (POST /trips)
func (api API) PostTrips(w http.ResponseWriter, r *http.Request) *spec.Response {
	var body spec.CreateTripRequest
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	tripID, err := api.store.CreateTrip(r.Context(), api.pool, body)
	if err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "failed to create trip, try again"})
	}

	go func() {
		if err := api.mailer.SendConfirmTripEmailToTripOwner(tripID); err != nil {
			api.logger.Error(
				"failed to send email on PostTrips",
				zap.Error(err),
				zap.String("trip_id", tripID.String()),
			)
		}
	}()

	return spec.PostTripsJSON201Response(spec.CreateTripResponse{TripID: tripID.String()})
}

// GetTripsTripID Get a trip details.
// (GET /trips/{tripId})
func (api API) GetTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.GetTripsTripIDJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	trip, err := api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	return spec.GetTripsTripIDJSON200Response(spec.GetTripDetailsResponse{
		Trip: spec.GetTripDetailsResponseTripObj{
			Destination: trip.Destination,
			EndsAt:      trip.EndsAt.Time,
			ID:          trip.ID.String(),
			IsConfirmed: trip.IsConfirmed,
			StartsAt:    trip.StartsAt.Time,
		},
	})
}

// PutTripsTripID Update a trip.
// (PUT /trips/{tripId})
func (api API) PutTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	var body spec.UpdateTripRequest
	err = json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	_, err = api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	err = api.store.UpdateTrip(r.Context(), pgstore.UpdateTripParams{
		Destination: body.Destination,
		EndsAt:      pgtype.Timestamp{Valid: true, Time: body.EndsAt},
		StartsAt:    pgtype.Timestamp{Valid: true, Time: body.StartsAt},
		ID:          tripUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to update trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	return spec.PutTripsTripIDJSON204Response(nil)
}

// GetTripsTripIDActivities Get a trip activities.
// (GET /trips/{tripId}/activities)
func (api API) GetTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	_, err = api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	activitiesInDB, err := api.store.GetTripActivities(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "nenhuma atividade encontrada"})
		}
		api.logger.Error("failed to get activities", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "failed to get activities"})
	}

	activityMap := make(map[time.Time][]spec.GetTripActivitiesResponseInnerArray)
	for _, activity := range activitiesInDB {
		date := activity.OccursAt.Time
		activityMap[date] = append(activityMap[date], spec.GetTripActivitiesResponseInnerArray{
			ID:       activity.ID.String(),
			OccursAt: activity.OccursAt.Time,
			Title:    activity.Title,
		})
	}

	var activities []spec.GetTripActivitiesResponseOuterArray
	for date, innerActivities := range activityMap {
		activities = append(activities, spec.GetTripActivitiesResponseOuterArray{
			Activities: innerActivities,
			Date:       date,
		})
	}

	return spec.GetTripsTripIDActivitiesJSON200Response(spec.GetTripActivitiesResponse{
		Activities: activities,
	})
}

// PostTripsTripIDActivities Create a trip activity.
// (POST /trips/{tripId}/activities)
func (api API) PostTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	var body spec.CreateActivityRequest

	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	err = json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	activityId, err := api.store.CreateActivity(r.Context(), pgstore.CreateActivityParams{
		TripID:   tripUUID,
		Title:    body.Title,
		OccursAt: pgtype.Timestamp{Valid: true, Time: body.OccursAt},
	})
	if err != nil {
		return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{Message: "failed to create trip activity, try again"})
	}

	return spec.PostTripsTripIDActivitiesJSON201Response(spec.CreateActivityResponse{
		ActivityID: activityId.String(),
	})
}

// GetTripsTripIDConfirm Confirm a trip and send e-mail invitations.
// (GET /trips/{tripId}/confirm)
func (api API) GetTripsTripIDConfirm(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.GetTripsTripIDConfirmJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	trip, err := api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	if trip.IsConfirmed {
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{Message: "trip already confirmed"})
	}

	err = api.store.ConfirmTrip(r.Context(), trip.ID)
	if err != nil {
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{Message: "failed to confirm trip, try again"})
	}

	go func() {
		// TODO: Implementar email de convite para participantes
		//if err := api.mailer.SendConfirmTripEmailToTripOwner(tripUUID); err != nil {
		//	api.logger.Error(
		//		"failed to send email on PostTrips",
		//		zap.Error(err),
		//		zap.String("trip_id", tripUUID.String()),
		//	)
		//}
	}()

	return spec.GetTripsTripIDConfirmJSON204Response(nil)
}

// PostTripsTripIDInvites Invite someone to the trip.
// (POST /trips/{tripId}/invites)
func (api API) PostTripsTripIDInvites(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	var body spec.InviteParticipantRequest

	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	err = json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	trip, err := api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	_, err = api.store.InviteParticipantToTrip(r.Context(), pgstore.InviteParticipantToTripParams{
		TripID: trip.ID,
		Email:  string(body.Email),
	})
	if err != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "failed to invite user to trip, try again"})
	}

	go func() {
		// TODO: Implementar email de convite para participante
		//if err := api.mailer.SendConfirmTripEmailToTripOwner(tripUUID); err != nil {
		//	api.logger.Error(
		//		"failed to send email on PostTrips",
		//		zap.Error(err),
		//		zap.String("trip_id", tripUUID.String()),
		//	)
		//}
	}()

	return spec.PostTripsTripIDInvitesJSON201Response(nil)
}

// GetTripsTripIDLinks Get a trip links.
// (GET /trips/{tripId}/links)
func (api API) GetTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	_, err = api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDLinksJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	linksInDB, err := api.store.GetTripLinks(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDLinksJSON400Response(spec.Error{Message: "nenhum link encontrado"})
		}
		api.logger.Error("failed to get links", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{Message: "failed to get links"})
	}

	var links []spec.GetLinksResponseArray
	for _, link := range linksInDB {
		links = append(links, spec.GetLinksResponseArray{
			ID:    link.ID.String(),
			Title: link.Title,
			URL:   link.Url,
		})
	}

	return spec.GetTripsTripIDLinksJSON200Response(spec.GetLinksResponse{
		Links: links,
	})
}

// PostTripsTripIDLinks Create a trip link.
// (POST /trips/{tripId}/links)
func (api API) PostTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	var body spec.CreateLinkRequest

	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	err = json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	_, err = api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	linkID, err := api.store.CreateTripLink(r.Context(), pgstore.CreateTripLinkParams{
		TripID: tripUUID,
		Title:  body.Title,
		Url:    body.URL,
	})
	if err != nil {
		api.logger.Error("failed to create link", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "failed to create link"})
	}

	return spec.PostTripsTripIDLinksJSON201Response(spec.CreateLinkResponse{
		LinkID: linkID.String(),
	})
}

// GetTripsTripIDParticipants Get a trip participants.
// (GET /trips/{tripId}/participants)
func (api API) GetTripsTripIDParticipants(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, err := uuid.Parse(tripID)
	if err != nil {
		spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	_, err = api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{Message: "viagem não encontrada"})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{Message: "invalid tripID"})
	}

	participantsInDB, err := api.store.GetParticipants(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{Message: "nenhum participante encontrado"})
		}
		api.logger.Error("failed to get participants", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{Message: "failed to get participants"})
	}

	var participants []spec.GetTripParticipantsResponseArray
	for _, participant := range participantsInDB {
		participants = append(participants, spec.GetTripParticipantsResponseArray{
			Email:       types.Email(participant.Email),
			ID:          participant.ID.String(),
			IsConfirmed: participant.IsConfirmed,
			// TODO: Implementar campo nome para participantes
			Name: nil,
		})
	}

	return spec.GetTripsTripIDParticipantsJSON200Response(spec.GetTripParticipantsResponse{
		Participants: participants,
	})
}
