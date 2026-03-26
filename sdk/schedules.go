package gamejanitor

import "context"

// ScheduleService handles schedule-related API calls for gameservers.
type ScheduleService struct {
	client *Client
}

// List returns all schedules for a gameserver.
func (s *ScheduleService) List(ctx context.Context, gameserverID string) ([]Schedule, error) {
	var schedules []Schedule
	if err := s.client.get(ctx, "/api/gameservers/"+gameserverID+"/schedules", &schedules); err != nil {
		return nil, err
	}
	return schedules, nil
}

// Get returns a single schedule.
func (s *ScheduleService) Get(ctx context.Context, gameserverID, scheduleID string) (*Schedule, error) {
	var schedule Schedule
	if err := s.client.get(ctx, "/api/gameservers/"+gameserverID+"/schedules/"+scheduleID, &schedule); err != nil {
		return nil, err
	}
	return &schedule, nil
}

// Create creates a new schedule on a gameserver.
func (s *ScheduleService) Create(ctx context.Context, gameserverID string, req *CreateScheduleRequest) (*Schedule, error) {
	var schedule Schedule
	if err := s.client.post(ctx, "/api/gameservers/"+gameserverID+"/schedules", req, &schedule); err != nil {
		return nil, err
	}
	return &schedule, nil
}

// Update partially updates a schedule.
func (s *ScheduleService) Update(ctx context.Context, gameserverID, scheduleID string, req *UpdateScheduleRequest) (*Schedule, error) {
	var schedule Schedule
	if err := s.client.patch(ctx, "/api/gameservers/"+gameserverID+"/schedules/"+scheduleID, req, &schedule); err != nil {
		return nil, err
	}
	return &schedule, nil
}

// Delete deletes a schedule.
func (s *ScheduleService) Delete(ctx context.Context, gameserverID, scheduleID string) error {
	return s.client.delete(ctx, "/api/gameservers/"+gameserverID+"/schedules/"+scheduleID)
}
