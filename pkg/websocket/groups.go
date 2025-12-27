package websocket

import (
	"fmt"
	"log"
	"sync/atomic"
)

// JoinGroup adds a client to a group
func (s *Server) JoinGroup(clientID, groupName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[clientID]
	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}

	// Add to server groups
	if _, ok := s.groups[groupName]; !ok {
		s.groups[groupName] = make(map[string]*Client)
	}
	s.groups[groupName][clientID] = client

	// Add to client groups
	client.mu.Lock()
	client.Groups[groupName] = true
	client.mu.Unlock()

	log.Printf("Client %s joined group %s", clientID, groupName)
	return nil
}

// LeaveGroup removes a client from a group
func (s *Server) LeaveGroup(clientID, groupName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[clientID]
	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}

	// Remove from server groups
	if group, ok := s.groups[groupName]; ok {
		delete(group, clientID)
		if len(group) == 0 {
			delete(s.groups, groupName)
		}
	}

	// Remove from client groups
	client.mu.Lock()
	delete(client.Groups, groupName)
	client.mu.Unlock()

	log.Printf("Client %s left group %s", clientID, groupName)
	return nil
}

// LeaveAllGroups removes a client from all groups
func (s *Server) LeaveAllGroups(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[clientID]
	if !ok {
		return
	}

	// Remove from all server groups
	client.mu.RLock()
	groups := make([]string, 0, len(client.Groups))
	for groupName := range client.Groups {
		groups = append(groups, groupName)
	}
	client.mu.RUnlock()

	for _, groupName := range groups {
		if group, ok := s.groups[groupName]; ok {
			delete(group, clientID)
			if len(group) == 0 {
				delete(s.groups, groupName)
			}
		}
	}

	// Clear client groups
	client.mu.Lock()
	client.Groups = make(map[string]bool)
	client.mu.Unlock()
}

// GetGroupMembers returns all clients in a group
func (s *Server) GetGroupMembers(groupName string) []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	group, ok := s.groups[groupName]
	if !ok {
		return nil
	}

	members := make([]*Client, 0, len(group))
	for _, client := range group {
		members = append(members, client)
	}
	return members
}

// GetGroupMemberIDs returns all client IDs in a group
func (s *Server) GetGroupMemberIDs(groupName string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	group, ok := s.groups[groupName]
	if !ok {
		return nil
	}

	ids := make([]string, 0, len(group))
	for id := range group {
		ids = append(ids, id)
	}
	return ids
}

// BroadcastToGroup sends a message to all clients in a group
func (s *Server) BroadcastToGroup(groupName string, message Message) error {
	s.mu.RLock()
	group, ok := s.groups[groupName]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("group %s not found", groupName)
	}

	// Copy clients to avoid holding lock during send
	clients := make([]*Client, 0, len(group))
	for _, client := range group {
		clients = append(clients, client)
	}
	s.mu.RUnlock()

	// Send to all clients in group
	sent := 0
	for _, client := range clients {
		select {
		case client.Send <- message:
			sent++
		default:
			log.Printf("Client %s send channel full, skipping message", client.ID)
		}
	}

	atomic.AddInt64(&s.stats.MessagesSent, int64(sent))
	return nil
}

// SendToGroup sends a message to specific clients in a group
func (s *Server) SendToGroup(groupName string, message Message) error {
	return s.BroadcastToGroup(groupName, message)
}

// SendToOthersInGroup sends a message to all clients in a group except the sender
func (s *Server) SendToOthersInGroup(groupName, senderID string, message Message) error {
	s.mu.RLock()
	group, ok := s.groups[groupName]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("group %s not found", groupName)
	}

	// Copy clients except sender
	clients := make([]*Client, 0, len(group)-1)
	for id, client := range group {
		if id != senderID {
			clients = append(clients, client)
		}
	}
	s.mu.RUnlock()

	// Send to all clients except sender
	sent := 0
	for _, client := range clients {
		select {
		case client.Send <- message:
			sent++
		default:
			log.Printf("Client %s send channel full, skipping message", client.ID)
		}
	}

	atomic.AddInt64(&s.stats.MessagesSent, int64(sent))
	return nil
}

// GetGroups returns all group names
func (s *Server) GetGroups() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groups := make([]string, 0, len(s.groups))
	for name := range s.groups {
		groups = append(groups, name)
	}
	return groups
}

// GetGroupCount returns the number of groups
func (s *Server) GetGroupCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.groups)
}

// IsGroupEmpty checks if a group has no members
func (s *Server) IsGroupEmpty(groupName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	group, ok := s.groups[groupName]
	if !ok {
		return true
	}
	return len(group) == 0
}
