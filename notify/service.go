package notify

import (
	"encoding/json"
	"log"

	"github.com/containrrr/shoutrrr"
	"github.com/tionis/hogs/database"
)

type Service struct {
	Store *database.Store
}

func NewService(store *database.Store) *Service {
	return &Service{Store: store}
}

func (s *Service) Send(eventType, message string) {
	channels, err := s.Store.ListNotificationChannels()
	if err != nil {
		log.Printf("notify: failed to list channels: %v", err)
		return
	}

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if !matchesEvent(ch.Events, eventType) {
			continue
		}
		go sendOne(ch, message)
	}
}

func matchesEvent(events json.RawMessage, eventType string) bool {
	if len(events) == 0 || string(events) == "[]" {
		return true
	}
	var eventList []string
	if err := json.Unmarshal(events, &eventList); err != nil {
		return true
	}
	for _, e := range eventList {
		if e == eventType || e == "*" {
			return true
		}
	}
	return false
}

func sendOne(ch database.NotificationChannel, message string) {
	if err := shoutrrr.Send(ch.URL, message); err != nil {
		log.Printf("notify: failed to send to %q (%s): %v", ch.Name, ch.Type, err)
		return
	}
	log.Printf("notify: sent to %q (%s)", ch.Name, ch.Type)
}
