package auth

// Passkeys: WebAuthn discoverable credentials, through go-webauthn.
//
// Relying party. The RP ID and origin come from CHADASH_PUBLIC_URL. Without it
// they're derived from the request's Host, but only for localhost and
// *.localhost: that's the dev setup, where `bun run dev` serves the SPA at
// http://localhost:5173 and proxies /api to the hub with Host intact. Browsers
// allow WebAuthn only over HTTPS or on localhost, and never on an IP address,
// so anywhere else the passkey routes answer with an error that says what to set.
//
// Ceremonies. Each begin keeps the library's SessionData in memory for
// ceremonyTTL, under a random ID carried in an HttpOnly cookie scoped to that
// ceremony's routes. Finish takes it out first, so a challenge is good for one
// attempt: a replayed, late or second response finds nothing.
//
// Credentials are discoverable (resident keys), so sign-in is usernameless:
// the authenticator returns the user handle with the assertion.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	// ceremonyTTL is how long a begun registration or sign-in waits for its
	// finish. It's also the timeout the browser is given.
	ceremonyTTL = 5 * time.Minute
	// maxCeremonies bounds the pending ceremonies kept in memory; past it, the
	// one closest to expiring is dropped.
	maxCeremonies  = 1024
	ceremonyCookie = "chadash_passkey"

	maxPasskeyName = 64 // characters
	rpName         = "Cha Dash"
	userHandleLen  = 32 // bytes
	maxTransports  = 8
)

var (
	ErrPasskeyNotFound  = errors.New("auth: no such passkey")
	errPasskeyExists    = errors.New("auth: passkey already registered")
	errLastSignInMethod = errors.New("auth: that's the account's last way to sign in")
	errUnknownPasskey   = errors.New("auth: unknown passkey")
	errDisabledUser     = errors.New("auth: account disabled")
)

// unavailableError says why passkeys can't work at the address in use.
type unavailableError struct{ msg string }

func (e *unavailableError) Error() string { return e.msg }

// relyingParty is the WebAuthn relying party a ceremony runs under.
type relyingParty struct {
	ID     string // effective domain, e.g. dash.example.com
	Origin string // scheme://host[:port]
}

// publicRelyingParty derives the relying party from CHADASH_PUBLIC_URL. A URL
// that's well formed but can't host passkeys (plain http off localhost, an IP
// address) isn't an error, because the URL may serve other purposes; why says
// what's wrong, and the passkey routes repeat it.
func publicRelyingParty(raw string) (rp relyingParty, why string, err error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return rp, "", fmt.Errorf("auth: public URL %q: want http(s)://host[:port]", raw)
	}
	host := strings.ToLower(u.Hostname())
	rp = relyingParty{ID: host, Origin: u.Scheme + "://" + strings.ToLower(u.Host)}
	switch {
	case net.ParseIP(host) != nil:
		return rp, "passkeys need a domain name, and CHADASH_PUBLIC_URL is an IP address; set it to the https:// name you open Cha Dash at", nil
	case u.Scheme != "https" && !isLocalhost(host):
		return rp, "passkeys need HTTPS, and CHADASH_PUBLIC_URL is plain http; set it to the https:// address you open Cha Dash at", nil
	}
	if err := protocol.ValidateRPID(host); err != nil {
		return rp, fmt.Sprintf("CHADASH_PUBLIC_URL's host %q can't be a passkey relying party: %v", host, err), nil
	}
	return rp, "", nil
}

func isLocalhost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// relyingParty resolves the relying party for a request, or explains why
// passkeys can't work at this address.
func (s *Service) relyingParty(r *http.Request) (relyingParty, error) {
	var rp relyingParty
	if s.publicURL != "" {
		if s.publicWhy != "" {
			return rp, &unavailableError{s.publicWhy}
		}
		rp = s.publicRP
	} else {
		hostport := strings.ToLower(r.Host)
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			host, port = strings.Trim(hostport, "[]"), ""
		}
		scheme := "http"
		if s.proxies.IsHTTPS(r) {
			scheme = "https"
		}
		switch ip := net.ParseIP(host); {
		case isLocalhost(host):
			rp = relyingParty{ID: host, Origin: scheme + "://" + hostport}
		case ip != nil && ip.IsLoopback():
			local := scheme + "://localhost"
			if port != "" {
				local += ":" + port
			}
			return rp, &unavailableError{fmt.Sprintf("passkeys need a domain name, not an IP address: open Cha Dash at %s instead, or set CHADASH_PUBLIC_URL", local)}
		default:
			return rp, &unavailableError{"passkeys need CHADASH_PUBLIC_URL set to the https:// address you open Cha Dash at; browsers allow them only over HTTPS or on localhost"}
		}
	}
	// Browsers send Origin on every POST. A page at another address would have
	// its ceremony refused by the browser anyway; say why here instead.
	if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, rp.Origin) {
		return rp, &unavailableError{fmt.Sprintf("passkeys work at %s, but this page is at %s; open Cha Dash at %s, or change CHADASH_PUBLIC_URL", rp.Origin, o, rp.Origin)}
	}
	return rp, nil
}

// webauthn builds the library's view of rp. It's cheap, and per request
// because the relying party can depend on the request (localhost).
func (rp relyingParty) webauthn() (*webauthn.WebAuthn, error) {
	discoverable := true
	timeout := webauthn.TimeoutConfig{Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL}
	return webauthn.New(&webauthn.Config{
		RPID:                  rp.ID,
		RPDisplayName:         rpName,
		RPOrigins:             []string{rp.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: &discoverable,
			UserVerification:   protocol.VerificationPreferred,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

// ── Ceremonies ───────────────────────────────────────────────────────────────

type ceremonyKind string

const (
	ceremonyRegister ceremonyKind = "register"
	ceremonyLogin    ceremonyKind = "login"
)

type ceremony struct {
	kind    ceremonyKind
	rp      relyingParty
	session webauthn.SessionData
	userID  string // registration: the signed-in user who began it
	expires time.Time
}

// ceremonies holds begun ceremonies in memory. The zero value is ready to use.
type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

// put stores c and returns its ID, sweeping out expired ceremonies on the way.
func (cs *ceremonies) put(now time.Time, c ceremony) string {
	id := rand.Text()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.m == nil {
		cs.m = map[string]ceremony{}
	}
	soonest := ""
	for k, v := range cs.m {
		if !now.Before(v.expires) {
			delete(cs.m, k)
		} else if soonest == "" || v.expires.Before(cs.m[soonest].expires) {
			soonest = k
		}
	}
	if len(cs.m) >= maxCeremonies {
		delete(cs.m, soonest)
	}
	cs.m[id] = c
	return id
}

// take removes and returns the ceremony with this ID and kind, if it hasn't expired.
func (cs *ceremonies) take(now time.Time, id string, kind ceremonyKind) (ceremony, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.m[id]
	if !ok || c.kind != kind {
		return ceremony{}, false
	}
	delete(cs.m, id)
	if !now.Before(c.expires) {
		return ceremony{}, false
	}
	return c, true
}

func ceremonyPath(kind ceremonyKind) string { return "/api/auth/passkeys/" + string(kind) }

func (s *Service) beginCeremony(w http.ResponseWriter, r *http.Request, c ceremony) {
	now := s.now()
	c.expires = now.Add(ceremonyTTL)
	id := s.ceremonies.put(now, c)
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookie,
		Value:    id,
		Path:     ceremonyPath(c.kind),
		MaxAge:   int(ceremonyTTL / time.Second),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// takeCeremony ends the ceremony this browser began, whatever happens next.
func (s *Service) takeCeremony(w http.ResponseWriter, r *http.Request, kind ceremonyKind) (ceremony, bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookie,
		Path:     ceremonyPath(kind),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	c, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return ceremony{}, false
	}
	return s.ceremonies.take(s.now(), c.Value, kind)
}

// ── Storage ──────────────────────────────────────────────────────────────────

// Passkey is a registered credential as the API shows it.
type Passkey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// RPID is the relying party it was registered under; it signs in only there.
	RPID string `json:"rp_id"`
	// BackedUp is the BS flag: the passkey is synced or backed up.
	BackedUp bool `json:"backed_up"`
	// CloneWarning is set once its signature counter has gone backwards.
	CloneWarning bool `json:"clone_warning"`
}

// passkeyUser is a user as go-webauthn sees one.
type passkeyUser struct {
	user   User
	handle []byte
	creds  []webauthn.Credential
}

func (p *passkeyUser) WebAuthnID() []byte                         { return p.handle }
func (p *passkeyUser) WebAuthnName() string                       { return p.user.Username }
func (p *passkeyUser) WebAuthnDisplayName() string                { return p.user.Username }
func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

type scanner interface{ Scan(dest ...any) error }

const credentialColumns = `p.id, p.credential_id, p.public_key, p.sign_count, p.clone_warning, p.transports,
	p.aaguid, p.attachment, p.flags, p.attestation_type, p.attestation_format`

// scanCredential reads credentialColumns, then extra.
func scanCredential(sc scanner, extra ...any) (string, webauthn.Credential, error) {
	var (
		id, transports, attachment string
		signCount, flags           int64
		clone                      bool
		c                          webauthn.Credential
	)
	dest := append([]any{&id, &c.ID, &c.PublicKey, &signCount, &clone, &transports,
		&c.Authenticator.AAGUID, &attachment, &flags, &c.AttestationType, &c.AttestationFormat}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return "", c, err
	}
	var ts []protocol.AuthenticatorTransport
	if err := json.Unmarshal([]byte(transports), &ts); err != nil {
		return "", c, fmt.Errorf("passkey %s: transports: %w", id, err)
	}
	c.Transport = ts
	c.Authenticator.SignCount = uint32(signCount)
	c.Authenticator.CloneWarning = clone
	c.Authenticator.Attachment = protocol.AuthenticatorAttachment(attachment)
	c.Flags = webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(flags))
	return id, c, nil
}

// userHandle returns the user's WebAuthn user handle, creating it if asked.
func (s *Service) userHandle(ctx context.Context, userID string, create bool) ([]byte, error) {
	var h []byte
	err := s.st.Reader().QueryRowContext(ctx, `SELECT handle FROM passkey_users WHERE user_id = ?`, userID).Scan(&h)
	if !errors.Is(err, sql.ErrNoRows) || !create {
		return h, err
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		fresh := make([]byte, userHandleLen)
		rand.Read(fresh)
		if _, err := tx.ExecContext(ctx, `INSERT INTO passkey_users (user_id, handle, created_at) VALUES (?, ?, ?) ON CONFLICT (user_id) DO NOTHING`,
			userID, fresh, s.now().UnixMilli()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT handle FROM passkey_users WHERE user_id = ?`, userID).Scan(&h)
	})
	return h, err
}

// userCredentials returns the user's passkeys registered under rpID.
func (s *Service) userCredentials(ctx context.Context, userID, rpID string) ([]webauthn.Credential, error) {
	rows, err := s.st.Reader().QueryContext(ctx,
		`SELECT `+credentialColumns+` FROM passkeys p WHERE p.user_id = ? AND p.rp_id = ? ORDER BY p.created_at, p.rowid`, userID, rpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		_, c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// lookupPasskey finds the passkey a sign-in presents, with its owner.
func (s *Service) lookupPasskey(ctx context.Context, rpID string, credID []byte) (string, *passkeyUser, error) {
	pu := &passkeyUser{}
	var disabled sql.NullInt64
	id, c, err := scanCredential(s.st.Reader().QueryRowContext(ctx, `
		SELECT `+credentialColumns+`, u.id, u.username, u.role, u.disabled_at, h.handle
		FROM passkeys p
		JOIN users u ON u.id = p.user_id
		JOIN passkey_users h ON h.user_id = p.user_id
		WHERE p.credential_id = ? AND p.rp_id = ?`, credID, rpID),
		&pu.user.ID, &pu.user.Username, &pu.user.Role, &disabled, &pu.handle)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil, errUnknownPasskey
	case err != nil:
		return "", nil, fmt.Errorf("auth: %w", err)
	case disabled.Valid:
		return id, nil, errDisabledUser
	}
	pu.creds = []webauthn.Credential{c}
	return id, pu, nil
}

// savePasskey stores a newly registered credential under a default name.
func (s *Service) savePasskey(ctx context.Context, userID, rpID string, c *webauthn.Credential) (Passkey, error) {
	transports, err := json.Marshal(cleanTransports(c.Transport))
	if err != nil {
		return Passkey{}, err
	}
	var aaguid []byte
	if len(c.Authenticator.AAGUID) > 0 {
		aaguid = c.Authenticator.AAGUID
	}
	now := s.now()
	p := Passkey{
		ID:        "pk_" + strings.ToLower(rand.Text()),
		CreatedAt: time.UnixMilli(now.UnixMilli()).UTC(),
		RPID:      rpID,
		BackedUp:  c.Flags.BackupState,
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM passkeys WHERE credential_id = ?`, c.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errPasskeyExists
		}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM passkeys WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		var taken []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			taken = append(taken, name)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		p.Name = defaultPasskeyName(aaguid, taken)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO passkeys (id, user_id, rp_id, credential_id, public_key, sign_count, transports, aaguid, attachment,
				flags, backup_eligible, backup_state, attestation_type, attestation_format, name, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, userID, rpID, c.ID, c.PublicKey, int64(c.Authenticator.SignCount), string(transports), aaguid,
			string(c.Authenticator.Attachment), int64(c.Flags.ProtocolValue()), c.Flags.BackupEligible, c.Flags.BackupState,
			c.AttestationType, c.AttestationFormat, p.Name, now.UnixMilli())
		return err
	})
	if err != nil {
		return Passkey{}, err
	}
	return p, nil
}

// recordPasskeyUse writes back what a successful sign-in changed. The counter
// never moves backwards and a clone warning, once raised, stays.
func (s *Service) recordPasskeyUse(ctx context.Context, id string, c *webauthn.Credential) error {
	_, err := s.st.Writer().ExecContext(ctx, `
		UPDATE passkeys SET sign_count = max(sign_count, ?), clone_warning = max(clone_warning, ?),
			flags = ?, backup_state = ?, last_used_at = ?
		WHERE id = ?`,
		int64(c.Authenticator.SignCount), c.Authenticator.CloneWarning,
		int64(c.Flags.ProtocolValue()), c.Flags.BackupState, s.now().UnixMilli(), id)
	return err
}

const passkeyColumns = `id, name, created_at, last_used_at, rp_id, backup_state, clone_warning`

func scanPasskey(sc scanner) (Passkey, error) {
	var p Passkey
	var created int64
	var used sql.NullInt64
	if err := sc.Scan(&p.ID, &p.Name, &created, &used, &p.RPID, &p.BackedUp, &p.CloneWarning); err != nil {
		return Passkey{}, err
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	if used.Valid {
		t := time.UnixMilli(used.Int64).UTC()
		p.LastUsedAt = &t
	}
	return p, nil
}

// ListPasskeys returns the user's passkeys, oldest first (rowid breaks ties
// within a millisecond). Never nil.
func (s *Service) ListPasskeys(ctx context.Context, userID string) ([]Passkey, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+passkeyColumns+` FROM passkeys WHERE user_id = ? ORDER BY created_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RenamePasskey renames one of the user's passkeys.
func (s *Service) RenamePasskey(ctx context.Context, userID, id, name string) (Passkey, error) {
	name, err := cleanPasskeyName(name)
	if err != nil {
		return Passkey{}, err
	}
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return Passkey{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return Passkey{}, errors.Join(ErrPasskeyNotFound, err)
	}
	p, err := scanPasskey(s.st.Reader().QueryRowContext(ctx, `SELECT `+passkeyColumns+` FROM passkeys WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrPasskeyNotFound // deleted in between
	}
	return p, err
}

// DeletePasskey removes one of the user's passkeys, unless it's the last way
// they can sign in: no password and no other passkey.
func (s *Service) DeletePasskey(ctx context.Context, userID, id string) error {
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var mine, total int
		var hasPassword bool
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM passkeys WHERE id = ? AND user_id = ?),
			(SELECT count(*) FROM passkeys WHERE user_id = ?),
			coalesce((SELECT password_hash IS NOT NULL FROM users WHERE id = ?), 0)`,
			id, userID, userID, userID).Scan(&mine, &total, &hasPassword); err != nil {
			return err
		}
		if mine == 0 {
			return ErrPasskeyNotFound
		}
		if !hasPassword && total <= 1 {
			return errLastSignInMethod
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
		return err
	})
}

// cleanPasskeyName trims a passkey name and checks it's displayable.
func cleanPasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", &ValidationError{"name must not be empty"}
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl):
		return "", &ValidationError{"name must be plain text"}
	case utf8.RuneCountInString(name) > maxPasskeyName:
		return "", &ValidationError{fmt.Sprintf("name must be at most %d characters", maxPasskeyName)}
	}
	return name, nil
}

// cleanTransports keeps the transports WebAuthn defines. The client reports
// them unsigned; they're only hints for the browser at the next ceremony.
func cleanTransports(ts []protocol.AuthenticatorTransport) []protocol.AuthenticatorTransport {
	out := []protocol.AuthenticatorTransport{}
	for _, t := range ts {
		switch t {
		case protocol.USB, protocol.NFC, protocol.BLE, protocol.SmartCard, protocol.Hybrid, protocol.Internal:
			if !slices.Contains(out, t) && len(out) < maxTransports {
				out = append(out, t)
			}
		}
	}
	return out
}

// defaultPasskeyName names a new passkey after its provider when the AAGUID
// is a known one, otherwise "Passkey N", avoiding the user's existing names.
func defaultPasskeyName(aaguid []byte, taken []string) string {
	isTaken := func(name string) bool {
		return slices.ContainsFunc(taken, func(t string) bool { return strings.EqualFold(t, name) })
	}
	if base, ok := aaguidNames[formatAAGUID(aaguid)]; ok {
		name := base
		for i := 2; isTaken(name); i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
		return name
	}
	for i := len(taken) + 1; ; i++ {
		if name := fmt.Sprintf("Passkey %d", i); !isTaken(name) {
			return name
		}
	}
}

func formatAAGUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// aaguidNames are the passkey providers a default name can come from, from the
// community list at github.com/passkeydeveloper/passkey-authenticator-aaguids.
// Browsers report a zero AAGUID for many authenticators, and an unknown one just
// means "Passkey N", so this lists only common providers.
var aaguidNames = map[string]string{
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	"adce0002-35bc-c60a-648b-0b25f1f05503": "Chrome on Mac",
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"dd4ec289-e01d-41c9-bb89-70fa845d4bf2": "iCloud Keychain",
	"08987058-cadc-4b81-b6e1-30de50dcbe96": "Windows Hello",
	"9ddd1817-af5a-4672-a2b9-3e3dd95000a9": "Windows Hello",
	"6028b017-b1d4-4c02-b4b3-afcdafc96bb2": "Windows Hello",
	"bada5566-a7aa-401f-bd96-45619a55120d": "1Password",
	"d548826e-79b4-db40-a3d8-11116f7e8349": "Bitwarden",
	"531126d6-e717-415c-9320-3d9aa6981239": "Dashlane",
	"fdb141b2-5d84-443e-8a35-4698c205a502": "KeePassXC",
	"50726f74-6f6e-5061-7373-50726f746f6e": "Proton Pass",
	"53414d53-554e-4700-0000-000000000000": "Samsung Pass",
	"b84e4048-15dc-4dd0-8640-f4f60813c8af": "NordPass",
	"0ea242b4-43c4-4a1b-8b17-dd6d0b6baec6": "Keeper",
}
