package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---------- Config ----------

type Config struct {
	Listen        string `json:"listen"`
	BaseURL       string `json:"baseURL"`
	DataDir       string `json:"dataDir"`
	AdminHash     string `json:"adminPasswordHash"`
	SessionSecret string `json:"sessionSecret"`
}

func configPath() string {
	if p := os.Getenv("PI_CONFIG"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err == nil {
		c := filepath.Join(filepath.Dir(exe), "config.json")
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "config.json"
}

func loadConfig() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err == nil {
		var c Config
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	c, err := promptConfig()
	if err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	fmt.Printf("About to write config to %s:\n%s\nWrite it? [Y/n] ", path, b)
	rd := bufio.NewReader(os.Stdin)
	line, _ := rd.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "n" || line == "no" {
		return nil, errors.New("config not written; exiting")
	}
	os.WriteFile(path, b, 0o600)
	fmt.Println("Config written.")
	return c, nil
}

func promptConfig() (*Config, error) {
	rd := bufio.NewReader(os.Stdin)
	ask := func(prompt, def string) string {
		fmt.Printf("%s [%s]: ", prompt, def)
		line, _ := rd.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}
	c := &Config{}
	c.Listen = ask("Listen address", ":8080")
	c.BaseURL = ask("Public base URL", "http://localhost:8080")
	c.DataDir = ask("Data directory", "data")
	pw := ""
	for {
		fmt.Print("Set admin password (min 6 chars): ")
		l, _ := rd.ReadString('\n')
		pw = strings.TrimSpace(l)
		if len(pw) >= 6 {
			break
		}
		fmt.Println("Too short.")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	c.AdminHash = string(h)
	sec := make([]byte, 32)
	rand.Read(sec)
	c.SessionSecret = hex.EncodeToString(sec)
	return c, nil
}

// ---------- Data ----------

type RSVP struct {
	Coming    bool      `json:"coming"`
	Name      string    `json:"name"`
	Guests    int       `json:"guests"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Invitee struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Token    string      `json:"token"`
	Passcode string      `json:"passcode,omitempty"`
	RSVP     *RSVP       `json:"rsvp,omitempty"`
	CheckIns []time.Time `json:"checkIns,omitempty"`
}

type Party struct {
	ID                  string    `json:"id"`
	Slug                string    `json:"slug"`
	Title               string    `json:"title"`
	StartsAt            time.Time `json:"startsAt"`
	EndsAt              time.Time `json:"endsAt,omitempty"`
	Location            string    `json:"location"`
	GeneralLocation     string    `json:"generalLocation,omitempty"`
	Description         string    `json:"description,omitempty"`
	HeaderImage         string    `json:"headerImage,omitempty"`
	FlyerImage          string    `json:"flyerImage,omitempty"`
	InviteMode          string    `json:"inviteMode"` // public | password | passcode | link
	Password            string    `json:"password,omitempty"`
	PassStyle           string    `json:"passStyle,omitempty"` // chars | digits | words
	PassLen             int       `json:"passLen,omitempty"`
	MaxAdditionalGuests int       `json:"maxAdditionalGuests"`
	CheckInEnabled      bool      `json:"checkInEnabled"`
	GuestListPublic     bool      `json:"guestListPublic"`
	LocationPublic      bool      `json:"locationPublic"`
	ColorScheme         string    `json:"colorScheme"`
	Invitees            []Invitee `json:"invitees"`
}

type Store struct {
	parties  []*Party
	path     string
	filesDir string
}

func openStore(cfg *Config) (*Store, error) {
	s := &Store{
		path:     filepath.Join(cfg.DataDir, "party.json"),
		filesDir: filepath.Join(cfg.DataDir, "files"),
	}
	if err := os.MkdirAll(s.filesDir, 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.parties); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.parties, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) bySlug(slug string) *Party {
	for _, p := range s.parties {
		if p.Slug == slug {
			return p
		}
	}
	return nil
}
func (s *Store) byID(id string) *Party {
	for _, p := range s.parties {
		if p.ID == id {
			return p
		}
	}
	return nil
}
func (s *Store) byToken(token string) (*Party, *Invitee) {
	for _, p := range s.parties {
		for i := range p.Invitees {
			if p.Invitees[i].Token == token {
				return p, &p.Invitees[i]
			}
		}
	}
	return nil, nil
}

func randID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
func randCode() string {
	const chars = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

var wordlist = strings.Fields(`apple anchor autumn amber arrow beach basil blink bloom brave breeze butter cactus candle canyon cedar cherry chorus cinder citrus clover comet copper coral cosmic cotton crater crimson crystal daisy dawn delta diamond dolphin domino dragon dune ember evening falcon feather fern fiddle flame flint forest fossil fox galaxy garnet ginger glacier granite harbor harvest hazel heron honey horizon ivory jade jasmine jigsaw jungle juniper kernel lantern lava lemon lilac linen lotus lagoon lynx maple marigold meadow mint mirror mosaic mountain mulberry nebula nectar nightfall noble oat ocean olive onyx opal orbit orchid otter palm papaya pebble pepper petal piano picket pigeon pine pixel plum poppy prairie prism puffin quail quartz quicksand radish raven reef ripple river robin rocket rosemary ruby saffron sage sailor sandalwood sapphire satin sequoia shadow shrimp silk silver sketch slate snow soap solar spice spring spruce starling sunrise sunset syrup thistle thunder tiger timber topaz torch trellis tulip tundra turquoise unicorn vanilla velvet verse violet walnut waterfall whisper willow window winter wren zephyr zinc`)

// randomPass generates a passcode per style: chars/digits/words with given length/count.
func randomPass(style string, n int) string {
	if n < 1 {
		n = 8
	}
	switch style {
	case "digits":
		max := 1
		for i := 1; i < n; i++ {
			max *= 10
		}
		b := make([]byte, 4)
		rand.Read(b)
		v := int(binary(b)) % (max * 10)
		return fmt.Sprintf("%0*d", n, v)
	case "words":
		if n < 1 {
			n = 3
		}
		ws := make([]string, n)
		for i := range ws {
			b := make([]byte, 4)
			rand.Read(b)
			ws[i] = wordlist[int(binary(b))%len(wordlist)]
		}
		return strings.Join(ws, "-")
	default: // chars
		const pool = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*"
		b := make([]byte, n)
		rand.Read(b)
		for i := range b {
			b[i] = pool[int(b[i])%len(pool)]
		}
		return string(b)
	}
}

func binary(b []byte) uint32 {
	var v uint32
	for i, x := range b {
		if i >= 4 {
			break
		}
		v |= uint32(x) << (8 * i)
	}
	return v
}

// ---------- Sessions ----------

type Sessions struct{ secret []byte }

func (s *Sessions) sign(v string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}
func (s *Sessions) cookie(user string) *http.Cookie {
	val := user + "." + strconv.FormatInt(time.Now().Unix(), 10)
	return &http.Cookie{
		Name: "session", Value: val + "." + s.sign(val),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 86400 * 7,
	}
}
func (s *Sessions) check(r *http.Request) bool {
	c, err := r.Cookie("session")
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 {
		return false
	}
	val := parts[0] + "." + parts[1]
	if subtle.ConstantTimeCompare([]byte(s.sign(val)), []byte(parts[2])) != 1 {
		return false
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && time.Since(time.Unix(ts, 0)) < 7*24*time.Hour
}
