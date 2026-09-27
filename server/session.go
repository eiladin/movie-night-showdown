package server

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of a Session.
type Status string

const (
	StatusLobby   Status = "lobby"
	StatusActive  Status = "active"
	StatusMatched Status = "matched"
	StatusEnded   Status = "ended"
)

// Swipe records a participant's most recent vote, kept for undo.
type Swipe struct {
	MovieID string `json:"movieId"`
	Yes     bool   `json:"yes"`
}

// Participant is one device/person in a session. Token is never sent to
// other participants (json:"-"); it is only handed to its own owner, once,
// via the REST create-session response or a session_state message addressed
// to that participant.
type Participant struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsHost    bool   `json:"isHost"`
	Connected bool   `json:"connected"`
	// Ready is a guest's answer to the lobby ready gate. It persists across
	// disconnects and is never cleared by toggling the gate.
	Ready bool   `json:"ready"`
	Token string `json:"-"`
}

// Session is one in-memory "movie night".
type Session struct {
	Code          string
	HostID        string
	RequiredCount int
	Locked        bool
	// WaitForReady is the host's lobby gate: when set, the host may not
	// start until every non-host participant is ready.
	WaitForReady bool
	Status       Status
	Deck         []Movie
	Participants map[string]*Participant
	Votes        map[string]map[string]bool // movieID -> (participantID -> yes?)
	LastSwipe    map[string]Swipe           // participantID -> last swipe (for undo)
	WinnerID     string
	CreatedAt    time.Time

	// mu guards every mutable field above plus clients. Hold it for the
	// shortest time possible; never block on a channel send while holding it.
	mu      sync.Mutex
	clients map[string]*Client // participantID -> currently attached connection
}

// codeAlphabet excludes visually-confusable characters (0/O, 1/I).
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomCode() string {
	b := make([]byte, 4)
	for i := range b {
		b[i] = codeAlphabet[rand.Intn(len(codeAlphabet))]
	}
	return string(b)
}

// Store holds every live Session, keyed by join code.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
}

// NewStore creates a Store and starts its TTL sweeper goroutine.
func NewStore(ttl time.Duration) *Store {
	st := &Store{sessions: map[string]*Session{}, ttl: ttl}
	go st.sweepExpired()
	return st
}

// Create makes a new Session with a unique code and the host as its first
// (not-yet-connected) participant.
func (st *Store) Create(hostName string) *Session {
	st.mu.Lock()
	defer st.mu.Unlock()

	code := st.uniqueCodeLocked()
	hostID := uuid.NewString()

	session := &Session{
		Code:   code,
		HostID: hostID,
		Status: StatusLobby,
		Participants: map[string]*Participant{
			hostID: {ID: hostID, Name: hostName, IsHost: true, Token: uuid.NewString()},
		},
		Votes:     map[string]map[string]bool{},
		LastSwipe: map[string]Swipe{},
		CreatedAt: time.Now(),
		clients:   map[string]*Client{},
	}
	st.sessions[code] = session
	return session
}

// SetTTL replaces the session expiry the sweeper enforces.
//
// It exists because SESSION_TTL is settable at runtime: storing the new value
// on the config without propagating it here would report a change as applied
// while the sweeper kept using the value it was constructed with.
func (st *Store) SetTTL(ttl time.Duration) {
	st.mu.Lock()
	st.ttl = ttl
	st.mu.Unlock()
}

// Get looks up a Session by its join code.
func (st *Store) Get(code string) (*Session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.sessions[code]
	return s, ok
}

// uniqueCodeLocked generates a code not already in use. Caller must hold st.mu.
func (st *Store) uniqueCodeLocked() string {
	for {
		code := randomCode()
		if _, exists := st.sessions[code]; !exists {
			return code
		}
	}
}

// sweepExpired periodically removes sessions older than the configured TTL.
// Runs for the lifetime of the process; intended to be started via `go`.
func (st *Store) sweepExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		st.mu.Lock()
		now := time.Now()
		for code, s := range st.sessions {
			if now.Sub(s.CreatedAt) > st.ttl {
				s.Close()
				delete(st.sessions, code)
			}
		}
		st.mu.Unlock()
	}
}

// Close gracefully terminates all connected clients when the session expires.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		if c.conn != nil {
			c.conn.Close()
		}
	}
}

// --- POST /api/sessions ---

type createSessionRequest struct {
	HostName string `json:"hostName"`
}

type createSessionResponse struct {
	Code          string `json:"code"`
	JoinURL       string `json:"joinURL"`
	ParticipantID string `json:"participantId"`
	Token         string `json:"token"`
}

// handleCreateSession lets an host start a new session. The host becomes
// participant #1; the response carries the resume token their browser must
// keep (localStorage) and send back on the /ws connection.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.HostName = strings.TrimSpace(req.HostName)
	if req.HostName == "" {
		http.Error(w, "hostName is required", http.StatusBadRequest)
		return
	}

	session := s.store.Create(req.HostName)
	host := session.Participants[session.HostID]

	resp := createSessionResponse{
		Code:          session.Code,
		JoinURL:       strings.TrimRight(s.config().PublicURL, "/") + "/join/" + session.Code,
		ParticipantID: host.ID,
		Token:         host.Token,
	}

	log.Printf("session created: code=%s host=%s", session.Code, req.HostName)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// EndAll ends every session that is not already finished, broadcasting the
// leaderboard built from the votes cast so far along with the reason.
//
// It exists because a configuration change replaces the source set, and a
// session dealt from sources that no longer exist is not recoverable: its deck
// references movies whose posters can no longer be fetched. Ending the session
// with a leaderboard is the honest outcome — the votes cast were real, so the
// standings are still worth showing.
//
// The broadcast happens outside the lock, matching Session.broadcast's contract
// and keeping a slow client from stalling every other session's shutdown.
func (st *Store) EndAll(reason EndReason) int {
	st.mu.Lock()
	sessions := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		sessions = append(sessions, s)
	}
	st.mu.Unlock()

	type ending struct {
		session     *Session
		leaderboard []LeaderboardEntry
	}
	pending := make([]ending, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		if s.Status == StatusEnded || s.Status == StatusMatched {
			s.mu.Unlock()
			continue
		}
		s.Status = StatusEnded
		// A lobby session has no votes, so this is an empty leaderboard rather
		// than a nil-pointer hazard: buildLeaderboardLocked iterates the deck,
		// which is also empty before host:start.
		lb := s.buildLeaderboardLocked()
		s.mu.Unlock()
		pending = append(pending, ending{session: s, leaderboard: lb})
	}

	for _, e := range pending {
		e.session.broadcast("session_ended", SessionEndedPayload{
			Leaderboard: e.leaderboard,
			Reason:      reason,
		})
	}
	return len(pending)
}
