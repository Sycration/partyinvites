package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func icsEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\n", "\\n", "\r", "")
	return r.Replace(s)
}

func icsTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

func icsUID(p *Party) string {
	return fmt.Sprintf("%s@partyinvites", p.ID)
}

func buildICS(p *Party, inviteeName string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//partyinvites//EN\r\nCALSCALE:GREGORIAN\r\nMETHOD:PUBLISH\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", icsUID(p))
	b.WriteString("DTSTAMP:" + icsTime(time.Now()) + "\r\n")
	b.WriteString("DTSTART:" + icsTime(p.StartsAt) + "\r\n")
	end := p.EndsAt.Time()
	if end.IsZero() || !end.After(p.StartsAt) {
		end = p.StartsAt.Add(3 * time.Hour)
	}
	b.WriteString("DTEND:" + icsTime(end) + "\r\n")
	summary := p.Title
	if inviteeName != "" {
		summary += " — " + inviteeName
	}
	b.WriteString("SUMMARY:" + icsEscape(summary) + "\r\n")
	if p.LocationPublic && p.Location != "" {
		b.WriteString("LOCATION:" + icsEscape(p.Location) + "\r\n")
	} else if p.GeneralLocation != "" {
		b.WriteString("LOCATION:" + icsEscape(p.GeneralLocation) + "\r\n")
	}
	if p.Description != "" {
		b.WriteString("DESCRIPTION:" + icsEscape(p.Description) + "\r\n")
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/calendar/"), ".ics")
	p := s.store.bySlug(slug)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if p.InviteMode != "public" {
		g := s.resolveGuest(r)
		if g == nil || g.party.ID != p.ID {
			jsonErr(w, 403, "forbidden")
			return
		}
	}
	var inviteeName string
	if g := s.resolveGuest(r); g != nil && g.invitee != nil {
		inviteeName = g.invitee.Name
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`.ics"`)
	fmt.Fprint(w, buildICS(p, inviteeName))
}
