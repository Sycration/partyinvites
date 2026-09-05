package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg      *Config
	store    *Store
	sessions *Sessions
	hub      *Hub
	start    time.Time
}

// statusWriter captures the response code for request logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		t0 := time.Now()
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/files/") {
			return // skip noisy asset logs
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "status", sw.status, "ms", time.Since(t0).Milliseconds())
	})
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(s.store.filesDir))))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		servePage(w, "home.html")
	})
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		servePage(w, "admin.html")
	})

	mux.HandleFunc("/api/login", s.handleAPIPublic)
	mux.HandleFunc("/api/rsvp", s.handleAPIPublic)
	mux.HandleFunc("/api/checkin", s.handleAPIPublic)
	mux.HandleFunc("/api/admin/", s.handleAPIAdmin)
	mux.HandleFunc("/api/checkins", s.apiCheckInStream)

	mux.HandleFunc("/calendar/", s.handleCalendar)
	mux.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		s.serveGuest(w, r, false)
	})
	mux.HandleFunc("/i/", func(w http.ResponseWriter, r *http.Request) {
		s.serveGuest(w, r, true)
	})
	return logRequests(mux)
}

func servePage(w http.ResponseWriter, name string) {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.Error(w, "missing page", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// guestView is the JSON the guest page needs (hides private fields).
func (s *Server) guestView(p *Party, inv *Invitee, revealed bool) map[string]any {
	view := map[string]any{
		"slug":                p.Slug,
		"title":               p.Title,
		"startsAt":            p.StartsAt,
		"endsAt":              p.EndsAt,
		"description":         p.Description,
		"headerImage":         p.HeaderImage,
		"flyerImage":          p.FlyerImage,
		"inviteMode":          p.InviteMode,
		"maxAdditionalGuests": p.MaxAdditionalGuests,
		"checkInEnabled":      p.CheckInEnabled,
		"colorScheme":         p.ColorScheme,
		"guestListPublic":     p.GuestListPublic,
		"rsvp":                map[string]any{},
	}
	if p.LocationPublic {
		view["location"] = p.Location
	} else if revealed {
		view["location"] = p.Location
	} else if p.GeneralLocation != "" {
		view["location"] = p.GeneralLocation
		view["generalOnly"] = true
	}
	if p.GuestListPublic {
		list := []map[string]any{}
		for i := range p.Invitees {
			if r := p.Invitees[i].RSVP; r != nil && r.Coming {
				entry := map[string]any{"name": r.Name, "guests": r.Guests}
				list = append(list, entry)
			}
		}
		view["guestList"] = list
	}
	if inv != nil {
		view["inviteeName"] = inv.Name
		if inv.RSVP != nil {
			view["rsvp"] = inv.RSVP
		}
	}
	return view
}

// serveGuest renders the guest page shell; the JS fetches state via /p/.../state or inline.
func (s *Server) serveGuest(w http.ResponseWriter, r *http.Request, byToken bool) {
	var p *Party
	var inv *Invitee
	if byToken {
		tok := strings.TrimPrefix(r.URL.Path, "/i/")
		if tok == "" {
			http.NotFound(w, r)
			return
		}
		p, inv = s.store.byToken(tok)
		if p == nil {
			http.NotFound(w, r)
			return
		}
	} else {
		slug := strings.TrimPrefix(r.URL.Path, "/p/")
		p = s.store.bySlug(slug)
		if p == nil {
			http.NotFound(w, r)
			return
		}
		if p.InviteMode == "link" {
			http.NotFound(w, r)
			return
		}
		if p.InviteMode == "password" {
			code := r.URL.Query().Get("code")
			if code == "" || !strings.EqualFold(code, p.Password) {
				servePage(w, "code.html")
				return
			}
		}
		if p.InviteMode == "passcode" {
			code := strings.TrimSpace(r.URL.Query().Get("code"))
			found := false
			for i := range p.Invitees {
				if p.Invitees[i].Passcode != "" && strings.EqualFold(code, p.Invitees[i].Passcode) {
					inv = &p.Invitees[i]
					found = true
					break
				}
			}
			if !found {
				servePage(w, "code.html")
				return
			}
		}
	}
	// embed guest JSON into page so no extra auth roundtrip needed
	view := s.guestView(p, inv, false)
	serveGuestPage(w, p, inv, view)
}

func serveGuestPage(w http.ResponseWriter, p *Party, inv *Invitee, view map[string]any) {
	// same template as guest page, data injected
	b, _ := staticFS.ReadFile("static/guest.html")
	html := string(b)
	data, _ := json.Marshal(view)
	html = strings.Replace(html, "__PARTY_DATA__", string(data), 1)
	token := ""
	if inv != nil {
		token = inv.Token
	}
	html = strings.Replace(html, "__PARTY_TOKEN__", token, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}
