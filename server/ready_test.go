package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// readyTestSession creates a lobby session with a host and one guest, both
// attached through in-memory clients whose send buffers the tests read.
func readyTestSession(t *testing.T) (*Session, *Client, *Client) {
	t.Helper()
	st := &Store{sessions: map[string]*Session{}, ttl: time.Hour}
	session := st.Create("Host")
	host := attachTestClient(session, session.HostID)

	guest := &Participant{ID: "guest", Name: "Guest", Token: "guest-token", Connected: true}
	session.mu.Lock()
	session.Participants[guest.ID] = guest
	session.mu.Unlock()
	return session, host, attachTestClient(session, guest.ID)
}

func attachTestClient(session *Session, participantID string) *Client {
	src := &fakeSource{id: "fake", movies: []Movie{{ID: "m1", Title: "Arrival"}}}
	c := &Client{
		send:          make(chan []byte, 64),
		done:          make(chan struct{}),
		session:       session,
		sources:       map[SourceID]MovieSource{src.id: src},
		order:         []SourceID{src.id},
		participantID: participantID,
	}
	session.mu.Lock()
	session.clients[participantID] = c
	session.mu.Unlock()
	return c
}

func sendTo(t *testing.T, c *Client, msgType string, payload interface{}) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	c.handleMessage(Envelope{Type: msgType, Payload: raw})
}

// drain returns every message queued for c, in order.
func drain(t *testing.T, c *Client) []Envelope {
	t.Helper()
	var out []Envelope
	for {
		select {
		case raw := <-c.send:
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			out = append(out, env)
		default:
			return out
		}
	}
}

func findType(envs []Envelope, msgType string) *Envelope {
	for i := range envs {
		if envs[i].Type == msgType {
			return &envs[i]
		}
	}
	return nil
}

func sessionStatus(s *Session) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

func TestReadyToggleBroadcasts(t *testing.T) {
	session, host, guest := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	sendTo(t, guest, "ready", ReadyPayload{Ready: true})

	envs := drain(t, host)
	if len(envs) != 2 || envs[1].Type != "participant_update" {
		t.Fatalf("host received %+v, want two participant_update messages", envs)
	}
	var p ParticipantUpdatePayload
	if err := json.Unmarshal(envs[1].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.WaitForReady {
		t.Error("waitForReady = false, want the gate in the broadcast")
	}
	for _, v := range p.Participants {
		if v.ID == "guest" && !v.Ready {
			t.Error("guest not ready in broadcast")
		}
	}

	sendTo(t, guest, "ready", ReadyPayload{Ready: false})
	session.mu.Lock()
	ready := session.Participants["guest"].Ready
	session.mu.Unlock()
	if ready {
		t.Error("guest still ready after un-readying")
	}
}

func TestHostStartRejectedWhenGuestUnready(t *testing.T) {
	session, host, _ := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	drain(t, host)

	sendTo(t, host, "host:start", HostStartPayload{})
	env := findType(drain(t, host), "error")
	if env == nil || !strings.Contains(string(env.Payload), "not ready") {
		t.Fatalf("error = %v, want a not-ready rejection", env)
	}
	if got := sessionStatus(session); got != StatusLobby {
		t.Errorf("status = %q, want lobby", got)
	}
}

func TestHostStartAcceptedWhenAllReady(t *testing.T) {
	session, host, guest := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	sendTo(t, guest, "ready", ReadyPayload{Ready: true})
	sendTo(t, host, "host:start", HostStartPayload{})
	if got := sessionStatus(session); got != StatusActive {
		t.Errorf("status = %q, want active", got)
	}
}

func TestHostStartAcceptedWhenGateOff(t *testing.T) {
	session, host, _ := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: false})
	sendTo(t, host, "host:start", HostStartPayload{})
	if got := sessionStatus(session); got != StatusActive {
		t.Errorf("status = %q, want active", got)
	}
}

func TestHostStartCountsOfflineGuests(t *testing.T) {
	session, host, guest := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	session.removeClient(guest)
	sendTo(t, host, "host:start", HostStartPayload{})
	if got := sessionStatus(session); got != StatusLobby {
		t.Errorf("status = %q, want an offline unready guest to block start", got)
	}
}

func TestReadyRejectedFromHost(t *testing.T) {
	session, host, _ := readyTestSession(t)
	sendTo(t, host, "ready", ReadyPayload{Ready: true})
	if findType(drain(t, host), "error") == nil {
		t.Error("host ready was not rejected")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.Participants[session.HostID].Ready {
		t.Error("host ready flag was set")
	}
}

func TestReadyRejectedAfterStart(t *testing.T) {
	session, host, guest := readyTestSession(t)
	sendTo(t, host, "host:start", HostStartPayload{})
	drain(t, guest)
	sendTo(t, guest, "ready", ReadyPayload{Ready: true})
	if findType(drain(t, guest), "error") == nil {
		t.Error("ready after start was not rejected")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.Participants["guest"].Ready {
		t.Error("ready flag changed after start")
	}
}

func TestHostOptionsRejectedFromGuest(t *testing.T) {
	session, _, guest := readyTestSession(t)
	sendTo(t, guest, "host:options", HostOptionsPayload{WaitForReady: true})
	if findType(drain(t, guest), "error") == nil {
		t.Error("guest host:options was not rejected")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.WaitForReady {
		t.Error("guest changed the gate")
	}
}

func TestReadySurvivesReconnect(t *testing.T) {
	session, host, guest := readyTestSession(t)
	sendTo(t, host, "host:options", HostOptionsPayload{WaitForReady: true})
	sendTo(t, guest, "ready", ReadyPayload{Ready: true})
	session.removeClient(guest)

	again := &Client{
		send:    make(chan []byte, 64),
		done:    make(chan struct{}),
		session: session,
		token:   "guest-token",
	}
	sendTo(t, again, "join", JoinPayload{})
	env := findType(drain(t, again), "session_state")
	if env == nil {
		t.Fatal("no session_state after rejoin")
	}
	var state SessionStatePayload
	if err := json.Unmarshal(env.Payload, &state); err != nil {
		t.Fatal(err)
	}
	if !state.WaitForReady {
		t.Error("session_state lost waitForReady")
	}
	for _, p := range state.Participants {
		if p.ID == "guest" && !p.Ready {
			t.Error("guest lost ready flag across reconnect")
		}
	}
}
