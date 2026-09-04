package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func jsonWrite(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func jsonErr(w http.ResponseWriter, code int, msg string) {
	jsonWrite(w, code, map[string]string{"error": msg})
}

var imageExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif",
	"image/webp": ".webp", "image/avif": ".avif",
}

func saveUpload(s *Store, fh multipart.File) (string, error) {
	sniff := make([]byte, 512)
	n, _ := io.ReadFull(fh, sniff)
	ct := http.DetectContentType(sniff[:n])
	ext, ok := imageExt[ct]
	if !ok {
		return "", fmt.Errorf("not a supported image (%s)", ct)
	}
	name := randID(8) + ext
	f, err := os.Create(filepath.Join(s.filesDir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(sniff[:n]); err != nil {
		return "", err
	}
	_, err = io.Copy(f, fh)
	return name, err
}

func (s *Server) handleAPIAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.sessions.check(r) {
		jsonErr(w, 401, "unauthorized")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/"), "/")
	switch {
	case path == "parties" && r.Method == http.MethodGet:
		jsonWrite(w, 200, s.store.parties)
	case path == "parties" && r.Method == http.MethodPost:
		s.apiCreateParty(w, r)
	case strings.HasPrefix(path, "parties/") && len(path) > 8:
		s.apiParty(w, r, path[8:])
	case path == "upload" && r.Method == http.MethodPost:
		s.apiUpload(w, r)
	default:
		jsonErr(w, 404, "not found")
	}
}

func (s *Server) handleAPIPublic(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	switch path {
	case "login":
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "method not allowed")
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if bcrypt.CompareHashAndPassword([]byte(s.cfg.AdminHash), []byte(body.Password)) == nil {
			http.SetCookie(w, s.sessions.cookie("admin"))
			jsonWrite(w, 200, map[string]bool{"ok": true})
		} else {
			jsonErr(w, 401, "wrong password")
		}
	case "rsvp":
		s.apiRSVP(w, r)
	case "checkin":
		s.apiCheckIn(w, r)
	default:
		jsonErr(w, 404, "not found")
	}
}

func (s *Server) apiCreateParty(w http.ResponseWriter, r *http.Request) {
	var p Party
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if err := s.validateParty(&p); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	p.ID = randID(8)
	base := slugify(p.Title)
	if base == "" {
		base = "party"
	}
	slug := base
	for i := 2; s.store.bySlug(slug) != nil; i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	p.Slug = slug
	s.prepareParty(&p)
	s.store.parties = append(s.store.parties, &p)
	s.store.save()
	jsonWrite(w, 200, p)
}

func (s *Server) validateParty(p *Party) error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("title required")
	}
	if p.StartsAt.IsZero() {
		return fmt.Errorf("date & time required")
	}
	switch p.InviteMode {
	case "public", "password", "passcode", "link":
	default:
		return fmt.Errorf("invalid invite mode")
	}
	if p.InviteMode == "password" && strings.TrimSpace(p.Password) == "" {
		p.Password = randCode()
	}
	if p.InviteMode == "passcode" {
		switch p.PassStyle {
		case "chars", "digits", "words":
		default:
			return fmt.Errorf("invalid passcode style")
		}
		if p.PassLen < 1 {
			switch p.PassStyle {
			case "digits":
				p.PassLen = 4
			case "words":
				p.PassLen = 3
			default:
				p.PassLen = 8
			}
		}
		if p.PassLen > 64 {
			p.PassLen = 64
		}
		// assign passcodes to any invitee missing one; ensure uniqueness
		used := map[string]bool{}
		for i := range p.Invitees {
			used[strings.ToLower(p.Invitees[i].Passcode)] = true
		}
		for i := range p.Invitees {
			if strings.TrimSpace(p.Invitees[i].Passcode) == "" {
				for tries := 0; tries < 100; tries++ {
					code := randomPass(p.PassStyle, p.PassLen)
					if !used[strings.ToLower(code)] {
						p.Invitees[i].Passcode = code
						used[strings.ToLower(code)] = true
						break
					}
				}
				if p.Invitees[i].Passcode == "" {
					p.Invitees[i].Passcode = randID(6)
				}
			}
		}
	}
	if p.MaxAdditionalGuests < 0 {
		p.MaxAdditionalGuests = 0
	}
	switch p.ColorScheme {
	case "", "midnight", "sunset", "mint", "candy", "mono":
	default:
		return fmt.Errorf("invalid color scheme")
	}
	if p.ColorScheme == "" {
		p.ColorScheme = "midnight"
	}
	return nil
}

func (s *Server) prepareParty(p *Party) {
	for i := range p.Invitees {
		if p.Invitees[i].ID == "" {
			p.Invitees[i].ID = randID(8)
		}
		if p.Invitees[i].Token == "" {
			p.Invitees[i].Token = randID(10)
		}
	}
}

func (s *Server) apiParty(w http.ResponseWriter, r *http.Request, id string) {
	p := s.store.byID(id)
	if p == nil {
		jsonErr(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonWrite(w, 200, p)
	case http.MethodPut:
		var np Party
		if err := json.NewDecoder(r.Body).Decode(&np); err != nil {
			jsonErr(w, 400, err.Error())
			return
		}
		if err := s.validateParty(&np); err != nil {
			jsonErr(w, 400, err.Error())
			return
		}
		np.ID = p.ID
		np.Slug = p.Slug
		for i := range np.Invitees {
			if old := p.inviteeByID(np.Invitees[i].ID); old != nil {
				if np.Invitees[i].Token == "" {
					np.Invitees[i].Token = old.Token
				}
				np.Invitees[i].RSVP = old.RSVP
				np.Invitees[i].CheckIns = old.CheckIns
			}
		}
		s.prepareParty(&np)
		*p = np
		s.store.save()
		jsonWrite(w, 200, p)
	case http.MethodDelete:
		for i, q := range s.store.parties {
			if q.ID == id {
				s.store.parties = append(s.store.parties[:i], s.store.parties[i+1:]...)
				break
			}
		}
		s.store.save()
		jsonWrite(w, 200, map[string]bool{"ok": true})
	default:
		jsonErr(w, 405, "method not allowed")
	}
}

func (p *Party) inviteeByID(id string) *Invitee {
	for i := range p.Invitees {
		if p.Invitees[i].ID == id {
			return &p.Invitees[i]
		}
	}
	return nil
}

func (s *Server) apiUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		jsonErr(w, 400, "invalid upload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, 400, "missing file")
		return
	}
	defer file.Close()
	name, err := saveUpload(s.store, file)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	jsonWrite(w, 200, map[string]string{"file": name})
}

// ---------- guest RSVP & check-in ----------

type guestRef struct {
	party   *Party
	invitee *Invitee
}

// resolve guest from /i/{token} or public/password party
func (s *Server) resolveGuest(r *http.Request) *guestRef {
	if tok := r.URL.Query().Get("token"); tok != "" {
		p, inv := s.store.byToken(tok)
		if p != nil {
			return &guestRef{p, inv}
		}
		return nil
	}
	slug := r.URL.Query().Get("party")
	p := s.store.bySlug(slug)
	if p == nil {
		return nil
	}
	if p.InviteMode == "link" {
		return nil
	}
	if p.InviteMode == "password" {
		code := r.Header.Get("X-Party-Code")
		if code == "" || !strings.EqualFold(code, p.Password) {
			return nil
		}
	}
	if p.InviteMode == "passcode" {
		code := strings.TrimSpace(r.Header.Get("X-Party-Code"))
		if code == "" {
			return nil
		}
		for i := range p.Invitees {
			if p.Invitees[i].Passcode != "" && strings.EqualFold(code, p.Invitees[i].Passcode) {
				return &guestRef{p, &p.Invitees[i]}
			}
		}
		return nil
	}
	return &guestRef{p, nil}
}

func (s *Server) apiRSVP(w http.ResponseWriter, r *http.Request) {
	g := s.resolveGuest(r)
	if g == nil {
		jsonErr(w, 403, "forbidden")
		return
	}
	var body struct {
		Coming bool   `json:"coming"`
		Name   string `json:"name"`
		Guests int    `json:"guests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if body.Guests < 0 {
		body.Guests = 0
	}
	if g.party.MaxAdditionalGuests == 0 {
		body.Guests = 0
	} else if body.Guests > g.party.MaxAdditionalGuests {
		body.Guests = g.party.MaxAdditionalGuests
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		jsonErr(w, 400, "name required")
		return
	}
	if g.invitee != nil {
		g.invitee.RSVP = &RSVP{Coming: body.Coming, Name: name, Guests: body.Guests, UpdatedAt: time.Now()}
	} else {
		// shared-link guest: upsert by name
		for i := range g.party.Invitees {
			inv := &g.party.Invitees[i]
			if strings.EqualFold(inv.Name, name) {
				inv.RSVP = &RSVP{Coming: body.Coming, Name: name, Guests: body.Guests, UpdatedAt: time.Now()}
				g.invitee = inv
				break
			}
		}
		if g.invitee == nil {
			g.party.Invitees = append(g.party.Invitees, Invitee{
				ID: randID(8), Token: randID(10), Name: name,
				RSVP: &RSVP{Coming: body.Coming, Name: name, Guests: body.Guests, UpdatedAt: time.Now()},
			})
		}
	}
	s.store.save()
	jsonWrite(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiCheckIn(w http.ResponseWriter, r *http.Request) {
	g := s.resolveGuest(r)
	if g == nil {
		jsonErr(w, 403, "forbidden")
		return
	}
	if !g.party.CheckInEnabled {
		jsonErr(w, 400, "check-in disabled")
		return
	}
	now := time.Now()
	if now.UTC().Before(g.party.StartsAt.UTC().Add(-24 * time.Hour)) {
		jsonErr(w, 400, "too early")
		return
	}
	if g.invitee == nil {
		// shared-link guest: upsert
		for i := range g.party.Invitees {
			if !g.party.Invitees[i].RSVP.Coming {
				g.invitee = &g.party.Invitees[i]
				break
			}
		}
		if g.invitee == nil {
			g.party.Invitees = append(g.party.Invitees, Invitee{ID: randID(8), Token: randID(10), Name: "Guest"})
			g.invitee = &g.party.Invitees[len(g.party.Invitees)-1]
		}
	}
	g.invitee.CheckIns = append(g.invitee.CheckIns, now)
	s.store.save()
	s.hub.broadcast(g.invitee.Name)
	jsonWrite(w, 200, map[string]bool{"ok": true})
}
