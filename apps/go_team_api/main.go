package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	Addr                      string
	DatabaseDSN               string
	DatabaseSource            string
	DatabaseHost              string
	APIKeysRaw                string
	WebDir                    string
	MailWorkerDomain          string
	MailAdminPassword         string
	MailAddressDomain         string
	MailDefaultTimeoutSeconds int
	MailPollIntervalSeconds   int
	MailRecentSeconds         int
}

type Server struct {
	cfg     Config
	db      *sql.DB
	apiKeys map[string]string
}

type accountRow struct {
	ID             string   `json:"id"`
	Email          string   `json:"email"`
	AccountType    string   `json:"account_type"`
	TeamSubscribed bool     `json:"team_subscribed"`
	TokenAlive     bool     `json:"token_alive"`
	Status         string   `json:"status"`
	CPAFilename    string   `json:"cpa_filename"`
	Error          string   `json:"error"`
	Tags           []string `json:"tags"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type tokenProbeResult struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ExpiresAt    string `json:"expired,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
	CheckMethod  string `json:"check_method,omitempty"`
	TokenLen     int    `json:"token_len"`
}

const (
	codexTokenEndpoint = "https://auth.openai.com/oauth/token"
	codexClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
)

type teamListRow struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	AccountID      string `json:"account_id"`
	Name           string `json:"name"`
	CurrentMembers int    `json:"current_members"`
	MaxMembers     int    `json:"max_members"`
	Subscription   string `json:"subscription_plan"`
	ExpiresAt      string `json:"expires_at"`
	Status         string `json:"status"`
	AccountRole    string `json:"account_role"`
	OwnerAccountID string `json:"owner_account_id"`
	OwnerEmail     string `json:"owner_email"`
	JoinedCount    int    `json:"joined_count"`
	InvitedCount   int    `json:"invited_count"`
	UpdatedAt      string `json:"updated_at"`
}

type accountStatusItem struct {
	TeamSubscribed bool   `json:"team_subscribed"`
	UpdatedAt      string `json:"updated_at"`
	Source         string `json:"source"`
}

var errMailAddressNotFound = errors.New("mail address id not found")

func main() {
	dbDSN, dbSource := resolveDatabaseDSNFromEnv()
	cfg := Config{
		Addr:                      envOrDefault("GO_TEAM_API_ADDR", "127.0.0.1:18081"),
		DatabaseDSN:               dbDSN,
		DatabaseSource:            dbSource,
		DatabaseHost:              dsnHost(dbDSN),
		APIKeysRaw:                strings.TrimSpace(os.Getenv("API_KEYS")),
		WebDir:                    envOrDefault("GO_TEAM_API_WEB_DIR", "./web"),
		MailWorkerDomain:          envOrDefault("MAIL_WORKER_DOMAIN", "cf-temp-email.mengcenfay.workers.dev"),
		MailAdminPassword:         envOrDefault("MAIL_ADMIN_PASSWORD", ""),
		MailAddressDomain:         envOrDefault("MAIL_EMAIL_DOMAIN", "agibar.x10.mx"),
		MailDefaultTimeoutSeconds: envIntOrDefault("MAIL_OTP_TIMEOUT_SECONDS", 120),
		MailPollIntervalSeconds:   envIntOrDefault("MAIL_OTP_POLL_SECONDS", 3),
		MailRecentSeconds:         envIntOrDefault("MAIL_OTP_RECENT_SECONDS", 300),
	}
	if cfg.DatabaseDSN == "" {
		log.Fatal("database DSN is required (DATABASE_URL or SUPABASE_DB_URL)")
	}

	db, err := sql.Open("pgx", cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("open database failed: %v", err)
	}
	configureDBPool(db)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping database failed: %v", err)
	}
	if err := runMigrations(ctx, db); err != nil {
		log.Fatalf("run migrations failed: %v", err)
	}

	s := &Server{
		cfg:     cfg,
		db:      db,
		apiKeys: parseAPIKeys(cfg.APIKeysRaw),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/logs", s.handleCompatLogs)
	mux.HandleFunc("/accounts", s.handleCompatAccounts)
	mux.HandleFunc("/mark-subscribed", s.handleCompatMarkSubscribed)
	mux.HandleFunc("/run", s.handleCompatNotImplemented)
	mux.HandleFunc("/run-existing", s.handleCompatNotImplemented)
	mux.HandleFunc("/login-existing", s.handleCompatNotImplemented)
	mux.HandleFunc("/v1/accounts", s.handleAccountsList)
	mux.HandleFunc("/v1/accounts/import-txt", s.handleImportTXT)
	mux.HandleFunc("/v1/accounts/subscription-success", s.handleAccountSubscriptionSuccess)
	mux.HandleFunc("/v1/accounts/", s.handleAccountSubroutes)
	mux.HandleFunc("/v1/teams/import-owners", s.handleTeamOwnersImport)
	mux.HandleFunc("/v1/teams", s.handleTeamsRoot)
	mux.HandleFunc("/v1/teams/", s.handleTeamSubroutes)
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir(cfg.WebDir))))
	mux.HandleFunc("/ui", s.handleUIRedirect)
	mux.HandleFunc("/mvp_invite_workspace.html", s.handleUIRedirect)
	mux.HandleFunc("/", s.handleRoot)

	log.Printf("go_team_api listening on http://%s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, withCORS(mux)); err != nil {
		log.Fatalf("http server failed: %v", err)
	}
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS accounts_pool (
			email TEXT PRIMARY KEY,
			password TEXT NULL,
			account_identity TEXT NOT NULL DEFAULT 'normal',
			status TEXT NOT NULL DEFAULT 'unknown',
			token_len INTEGER NOT NULL DEFAULT 0,
			cpa_filename TEXT NULL,
			error TEXT NULL,
			access_token TEXT NULL,
			refresh_token TEXT NULL,
			id_token TEXT NULL,
			token_alive BOOLEAN NOT NULL DEFAULT FALSE,
			token_check_method TEXT NULL,
			token_expired_at TIMESTAMPTZ NULL,
			token_last_refresh TIMESTAMPTZ NULL,
			last_token_check TIMESTAMPTZ NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`ALTER TABLE accounts_pool ADD COLUMN IF NOT EXISTS account_identity TEXT NOT NULL DEFAULT 'normal';`,
		`CREATE TABLE IF NOT EXISTS keygen_runs (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			run_started_at TIMESTAMPTZ NULL,
			run_finished_at TIMESTAMPTZ NULL,
			mode TEXT NULL,
			register_total INTEGER NOT NULL DEFAULT 0,
			feed_ok INTEGER NOT NULL DEFAULT 0,
			feed_fail INTEGER NOT NULL DEFAULT 0,
			verify_found INTEGER NOT NULL DEFAULT 0,
			result_json TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_pool_status ON accounts_pool(status);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_pool_identity ON accounts_pool(account_identity);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_pool_token_alive ON accounts_pool(token_alive);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_pool_last_token_check ON accounts_pool(last_token_check DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_keygen_runs_started_at ON keygen_runs(run_started_at DESC);`,
		`CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			owner_account_id TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS team_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS subscription_plan TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NULL;`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS max_members INTEGER NOT NULL DEFAULT 6;`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS current_members INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS account_role TEXT NOT NULL DEFAULT 'account-owner';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS access_token TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS refresh_token TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS session_token TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS client_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE teams ADD COLUMN IF NOT EXISTS last_sync TIMESTAMPTZ NULL;`,
		`CREATE TABLE IF NOT EXISTS team_invitations (
			id TEXT PRIMARY KEY,
			team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'invited',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (team_id, account_id)
		);`,
		`ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_owner_account_id_fkey;`,
		`ALTER TABLE team_invitations DROP CONSTRAINT IF EXISTS team_invitations_account_id_fkey;`,
		`CREATE INDEX IF NOT EXISTS idx_invites_team ON team_invitations(team_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_team_invitations_account_id ON team_invitations(account_id);`,
	}
	for _, stmt := range sqls {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func parseAPIKeys(raw string) map[string]string {
	out := map[string]string{}
	for _, row := range strings.Split(raw, ",") {
		part := strings.TrimSpace(row)
		if part == "" {
			continue
		}
		p := strings.SplitN(part, ":", 2)
		if len(p) != 2 {
			continue
		}
		role := normalizeRole(p[0])
		key := strings.TrimSpace(p[1])
		if role == "" || key == "" {
			continue
		}
		out[key] = role
	}
	return out
}

func normalizeRole(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "admin":
		return "admin"
	case "operator":
		return "operator"
	case "viewer":
		return "viewer"
	default:
		return ""
	}
}

func roleLevel(role string) int {
	switch normalizeRole(role) {
	case "viewer":
		return 1
	case "operator":
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

func (s *Server) authRole(r *http.Request) string {
	if len(s.apiKeys) == 0 {
		return "admin"
	}
	k := strings.TrimSpace(r.Header.Get("X-Api-Key"))
	if k == "" {
		k = strings.TrimSpace(r.Header.Get("X-Service-Token"))
	}
	return s.apiKeys[k]
}

func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, need string) bool {
	role := s.authRole(r)
	if roleLevel(role) >= roleLevel(need) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"ok":    false,
		"error": "unauthorized",
	})
	return false
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Api-Key, X-Service-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"service":            "team-go-api",
		"db":                 "postgresql",
		"db_host":            s.cfg.DatabaseHost,
		"db_source":          s.cfg.DatabaseSource,
		"token_check_mode":   "refresh_only",
		"mail_fetch_enabled": strings.TrimSpace(s.cfg.MailWorkerDomain) != "" && strings.TrimSpace(s.cfg.MailAdminPassword) != "",
	})
}

func (s *Server) handleCompatLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "viewer") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"run_id":      0,
		"running":     false,
		"started_at":  0,
		"finished_at": 0,
		"after":       0,
		"next_index":  0,
		"lines":       []string{},
	})
}

func (s *Server) handleCompatAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "viewer") {
		return
	}
	rows, err := s.queryAccounts(r.Context(), 500, "", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for i, a := range rows {
		out = append(out, map[string]any{
			"email":             a.Email,
			"password_present":  true,
			"team_subscribed":   a.TeamSubscribed,
			"status_updated_at": a.UpdatedAt,
			"status_source":     "",
			"order":             i,
			"id":                a.ID,
			"account_type":      a.AccountType,
			"tags":              a.Tags,
			"token_alive":       a.TokenAlive,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                         true,
		"count":                      len(out),
		"accounts":                   out,
		"accounts_file_path":         "",
		"accounts_file_exists":       false,
		"account_status_file_path":   "",
		"account_status_file_exists": false,
	})
}

func (s *Server) handleCompatMarkSubscribed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Email          string `json:"email"`
		TeamSubscribed bool   `json:"team_subscribed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "email is required"})
		return
	}
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO accounts_pool (email, account_identity, status, updated_at)
		 VALUES ($1,$2,'manual',NOW())
		 ON CONFLICT (email) DO UPDATE
		 SET account_identity=EXCLUDED.account_identity,
		     updated_at=NOW()`,
		email, boolToPoolIdentity(body.TeamSubscribed))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"email":           email,
		"team_subscribed": body.TeamSubscribed,
		"updated_at":      time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleAccountSubscriptionSuccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}

	var body struct {
		Email            string `json:"email"`
		AccountID        string `json:"account_id"`
		TeamName         string `json:"team_name"`
		SubscriptionPlan string `json:"subscription_plan"`
		ExpiresAt        string `json:"expires_at"`
		MaxMembers       int    `json:"max_members"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		SessionToken     string `json:"session_token"`
		ClientID         string `json:"client_id"`
		Source           string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "email is required"})
		return
	}
	accessToken := strings.TrimSpace(body.AccessToken)
	if accessToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "access_token is required for active check"})
		return
	}
	activeTeam, err := s.fetchActiveTeamFromAccessToken(r.Context(), accessToken, strings.TrimSpace(body.AccountID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"email": email,
		})
		return
	}

	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "plugin_success_team_subscribed"
	}

	accountID := strings.TrimSpace(activeTeam.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(body.AccountID)
	}
	subscriptionPlan := strings.TrimSpace(activeTeam.SubscriptionPlan)
	if subscriptionPlan == "" {
		subscriptionPlan = strings.TrimSpace(body.SubscriptionPlan)
	}
	expiresAt := activeTeam.ExpiresAt
	if !expiresAt.Valid {
		if raw := strings.TrimSpace(body.ExpiresAt); raw != "" {
			ts, parseErr := time.Parse(time.RFC3339, raw)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expires_at must be RFC3339"})
				return
			}
			expiresAt = sql.NullTime{Time: ts.UTC(), Valid: true}
		}
	}

	identity := "team_member"
	if strings.Contains(strings.ToLower(strings.TrimSpace(activeTeam.AccountRole)), "owner") {
		identity = "team_owner"
	}
	now := time.Now().UTC()
	refreshToken := strings.TrimSpace(body.RefreshToken)
	idToken := strings.TrimSpace(body.IDToken)
	var refreshAny any
	if refreshToken != "" {
		refreshAny = refreshToken
	}
	var idAny any
	if idToken != "" {
		idAny = idToken
	}
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO accounts_pool (
			email, account_identity, status, token_len, access_token, refresh_token, id_token, token_alive, token_check_method,
			token_expired_at, token_last_refresh, last_token_check, error, updated_at
		)
		VALUES ($1,$2,'team_upgrade',$3,$4,$5,$6,true,$7,$8,$9,$10,NULL,NOW())
		ON CONFLICT (email) DO UPDATE
		SET account_identity=EXCLUDED.account_identity,
		    token_len=EXCLUDED.token_len,
		    access_token=EXCLUDED.access_token,
		    refresh_token=COALESCE(EXCLUDED.refresh_token, accounts_pool.refresh_token),
		    id_token=COALESCE(EXCLUDED.id_token, accounts_pool.id_token),
		    token_alive=true,
		    token_check_method=EXCLUDED.token_check_method,
		    token_expired_at=COALESCE(EXCLUDED.token_expired_at, accounts_pool.token_expired_at),
		    token_last_refresh=COALESCE(EXCLUDED.token_last_refresh, accounts_pool.token_last_refresh),
		    last_token_check=EXCLUDED.last_token_check,
		    error=NULL,
		    updated_at=NOW()
	`, email, identity, len(accessToken), accessToken, refreshAny, idAny, "subscription_success", expiresAt, now, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"email":             email,
		"owner_account_id":  email,
		"team_id":           "",
		"created_team":      false,
		"team_subscribed":   true,
		"account_type":      "team",
		"active_checked":    true,
		"account_id":        accountID,
		"subscription_plan": subscriptionPlan,
		"status_source":     source,
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
	})
}

type activeTeamInfo struct {
	AccountID        string
	TeamName         string
	AccountRole      string
	SubscriptionPlan string
	ExpiresAt        sql.NullTime
}

func (s *Server) fetchActiveTeamFromAccessToken(ctx context.Context, accessToken, preferredAccountID string) (*activeTeamInfo, error) {
	at := strings.TrimSpace(accessToken)
	if at == "" {
		return nil, fmt.Errorf("access_token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("Origin", "https://chatgpt.com")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("active check request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("active check failed with status %d", resp.StatusCode)
	}

	var payload struct {
		Accounts map[string]struct {
			Account struct {
				PlanType        string `json:"plan_type"`
				Name            string `json:"name"`
				AccountUserRole string `json:"account_user_role"`
			} `json:"account"`
			Entitlement struct {
				HasActiveSubscription bool   `json:"has_active_subscription"`
				SubscriptionPlan      string `json:"subscription_plan"`
				ExpiresAt             string `json:"expires_at"`
			} `json:"entitlement"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("active check decode failed: %w", err)
	}

	preferred := strings.TrimSpace(preferredAccountID)
	var picked *activeTeamInfo
	for aid, row := range payload.Accounts {
		if strings.ToLower(strings.TrimSpace(row.Account.PlanType)) != "team" {
			continue
		}
		if !row.Entitlement.HasActiveSubscription {
			continue
		}
		info := &activeTeamInfo{
			AccountID:        strings.TrimSpace(aid),
			TeamName:         strings.TrimSpace(row.Account.Name),
			AccountRole:      strings.TrimSpace(row.Account.AccountUserRole),
			SubscriptionPlan: strings.TrimSpace(row.Entitlement.SubscriptionPlan),
		}
		if raw := strings.TrimSpace(row.Entitlement.ExpiresAt); raw != "" {
			if ts, err := time.Parse(time.RFC3339, raw); err == nil {
				info.ExpiresAt = sql.NullTime{Time: ts.UTC(), Valid: true}
			}
		}
		if preferred != "" {
			if info.AccountID == preferred {
				return info, nil
			}
			continue
		}
		picked = info
		break
	}
	if preferred != "" {
		return nil, fmt.Errorf("account_id %s is not an active team account", preferred)
	}
	if picked == nil {
		return nil, fmt.Errorf("no active team subscription found")
	}
	return picked, nil
}

func (s *Server) handleCompatNotImplemented(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"ok":    false,
		"error": "not implemented in team-go-api; use legacy/extension_backend/api_server.py for stripe pipeline",
	})
}

func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "viewer") {
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if x, err := strconv.Atoi(raw); err == nil && x > 0 && x <= 2000 {
			limit = x
		}
	}
	accountType := strings.TrimSpace(r.URL.Query().Get("account_type"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	rows, err := s.queryAccounts(r.Context(), limit, accountType, tag)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"count":    len(rows),
		"accounts": rows,
	})
}

func (s *Server) queryAccounts(ctx context.Context, limit int, accountType, tag string) ([]accountRow, error) {
	where := []string{"1=1"}
	args := []any{}
	i := 1
	if accountType != "" {
		switch strings.ToLower(strings.TrimSpace(accountType)) {
		case "team":
			where = append(where, "LOWER(COALESCE(a.account_identity,'')) IN ('team_owner','team_member')")
		case "normal":
			where = append(where, "LOWER(COALESCE(a.account_identity,'')) IN ('normal','plus')")
		default:
			return []accountRow{}, nil
		}
	}
	if tag != "" {
		tagLower := strings.ToLower(strings.TrimSpace(tag))
		switch tagLower {
		case "team":
			where = append(where, "LOWER(COALESCE(a.account_identity,'')) IN ('team_owner','team_member')")
		case "normal", "invite_pool":
			where = append(where, "LOWER(COALESCE(a.account_identity,'')) IN ('normal','plus')")
		case "plus", "team_owner", "team_member":
			where = append(where, fmt.Sprintf("LOWER(COALESCE(a.account_identity,''))=$%d", i))
			args = append(args, tagLower)
			i++
		default:
			where = append(where, fmt.Sprintf("LOWER(COALESCE(a.status,''))=$%d", i))
			args = append(args, tagLower)
			i++
		}
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT
			a.email,
			a.email,
			COALESCE(a.account_identity, 'normal'),
			a.token_alive,
			COALESCE(a.status, ''),
			COALESCE(a.cpa_filename, ''),
			COALESCE(a.error, ''),
			TO_CHAR(a.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS created_at,
			TO_CHAR(a.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at
		FROM accounts_pool a
		WHERE %s
		ORDER BY a.updated_at DESC
		LIMIT $%d
	`, strings.Join(where, " AND "), i)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []accountRow{}
	for rows.Next() {
		var rec accountRow
		var identity string
		if err := rows.Scan(
			&rec.ID,
			&rec.Email,
			&identity,
			&rec.TokenAlive,
			&rec.Status,
			&rec.CPAFilename,
			&rec.Error,
			&rec.CreatedAt,
			&rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rec.AccountType = poolIdentityToAccountType(identity)
		rec.TeamSubscribed = poolIdentityTeamSubscribed(identity)
		rec.Tags = poolIdentityToTags(identity)
		out = append(out, rec)
	}
	return out, nil
}

func (s *Server) handleImportTXT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Path               string `json:"path"`
		Text               string `json:"text"`
		DefaultAccountType string `json:"default_account_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	src := body.Text
	if strings.TrimSpace(src) == "" {
		path := strings.TrimSpace(body.Path)
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path or text is required"})
			return
		}
		bs, err := os.ReadFile(path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		src = string(bs)
	}

	accType := strings.ToLower(strings.TrimSpace(body.DefaultAccountType))
	if accType == "" {
		accType = "normal"
	}
	if accType != "normal" && accType != "team" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "default_account_type must be normal/team"})
		return
	}

	type parsed struct {
		email    string
		password string
		identity string
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	records := []parsed{}
	for _, raw := range lines {
		row := strings.TrimSpace(raw)
		if row == "" || strings.HasPrefix(row, "#") {
			continue
		}
		parts := strings.Split(row, ":")
		if len(parts) < 2 {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(parts[0]))
		password := strings.TrimSpace(parts[1])
		if email == "" || password == "" {
			continue
		}
		itemType := accType
		tags := []string{}
		if len(parts) >= 3 {
			third := strings.TrimSpace(parts[2])
			if third == "normal" || third == "team" {
				itemType = third
			} else if third != "" {
				tags = append(tags, splitTags(strings.ReplaceAll(third, ";", ","))...)
			}
		}
		if len(parts) >= 4 {
			tags = append(tags, splitTags(strings.ReplaceAll(strings.TrimSpace(parts[3]), ";", ","))...)
		}
		tags = uniqueTags(append(tags, itemType))
		records = append(records, parsed{
			email:    email,
			password: password,
			identity: derivePoolIdentity(itemType, tags, "normal"),
		})
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	inserted := 0
	updated := 0
	for _, rec := range records {
		var existingEmail string
		row := tx.QueryRowContext(r.Context(), `SELECT email FROM accounts_pool WHERE lower(email)=lower($1)`, rec.email)
		_ = row.Scan(&existingEmail)
		if existingEmail == "" {
			inserted++
		} else {
			updated++
		}

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO accounts_pool (email, password, account_identity, status, token_len, error, updated_at)
			VALUES ($1,$2,$3,'imported',0,NULL,NOW())
			ON CONFLICT (email) DO UPDATE
			SET password=EXCLUDED.password,
			    account_identity=EXCLUDED.account_identity,
			    error=NULL,
			    updated_at=NOW()
		`, rec.email, rec.password, rec.identity)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"total":    len(records),
		"inserted": inserted,
		"updated":  updated,
	})
}

func (s *Server) handleAccountSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/accounts/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	accountID, _ := url.PathUnescape(strings.TrimSpace(parts[0]))
	accountID = strings.TrimSpace(accountID)
	action := strings.TrimSpace(parts[1])
	if accountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "account id is required"})
		return
	}

	switch action {
	case "tags":
		s.handleAccountTags(w, r, accountID)
		return
	case "credentials":
		s.handleAccountCredentials(w, r, accountID)
		return
	case "otp-fetch":
		s.handleAccountOTPFetch(w, r, accountID)
		return
	case "cpa-toggle":
		s.handleAccountCPAToggle(w, r, accountID)
		return
	case "mail-list":
		s.handleAccountMailList(w, r, accountID)
		return
	case "token-check":
		s.handleAccountTokenCheck(w, r, accountID)
		return
	case "delete":
		s.handleAccountDelete(w, r, accountID)
		return
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
}

func (s *Server) handleAccountCredentials(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}

	var rec struct {
		ID         string
		Email      string
		Password   string
		Identity   string
		TokenAlive bool
	}
	err := s.db.QueryRowContext(r.Context(), `
		SELECT
			a.email,
			a.email,
			a.password,
			COALESCE(a.account_identity, 'normal'),
			a.token_alive
		FROM accounts_pool a
		WHERE lower(a.email)=lower($1)
	`, accountID).Scan(
		&rec.ID,
		&rec.Email,
		&rec.Password,
		&rec.Identity,
		&rec.TokenAlive,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"account": map[string]any{
			"id":              rec.ID,
			"email":           rec.Email,
			"password":        rec.Password,
			"account_type":    poolIdentityToAccountType(rec.Identity),
			"team_subscribed": poolIdentityTeamSubscribed(rec.Identity),
			"token_alive":     rec.TokenAlive,
			"tags":            poolIdentityToTags(rec.Identity),
		},
	})
}

func (s *Server) handleAccountOTPFetch(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	timeoutSec := body.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = s.cfg.MailDefaultTimeoutSeconds
	}
	if timeoutSec < 15 {
		timeoutSec = 15
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	var email string
	err := s.db.QueryRowContext(r.Context(), `SELECT email FROM accounts_pool WHERE lower(email)=lower($1)`, accountID).Scan(&email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "account email is empty"})
		return
	}

	code, err := s.fetchLatestOTPByEmail(r.Context(), email, timeoutSec)
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"account_id": accountID,
			"email":      email,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"account_id": accountID,
		"email":      email,
		"otp_code":   code,
	})
}

func (s *Server) handleAccountCPAToggle(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var currentStatus, currentCPA string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(status,''), COALESCE(cpa_filename,'')
		FROM accounts_pool
		WHERE lower(email)=lower($1)
	`, accountID).Scan(&currentStatus, &currentCPA)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	isFed := strings.Contains(strings.ToLower(strings.TrimSpace(currentStatus)), "fed") || strings.TrimSpace(currentCPA) != ""
	action := strings.ToLower(strings.TrimSpace(body.Action))
	targetUp := false
	switch action {
	case "up":
		targetUp = true
	case "down":
		targetUp = false
	default:
		targetUp = !isFed
	}

	nextStatus := currentStatus
	nextCPA := currentCPA
	if targetUp {
		nextStatus = "fed"
		if strings.TrimSpace(nextCPA) == "" {
			nextCPA = "manual-fed"
		}
	} else {
		nextStatus = "ready"
		nextCPA = ""
	}

	_, err = s.db.ExecContext(r.Context(), `
		UPDATE accounts_pool
		SET status=$1, cpa_filename=$2, updated_at=NOW()
		WHERE lower(email)=lower($3)
	`, nextStatus, nextCPA, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"account_id":   accountID,
		"status":       nextStatus,
		"cpa_filename": nextCPA,
		"fed":          targetUp,
		"action":       map[bool]string{true: "up", false: "down"}[targetUp],
	})
}

func (s *Server) handleAccountMailList(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	limit := 30
	if s := strings.TrimSpace(r.URL.Query().Get("limit")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}
	if limit < 1 {
		limit = 1
	}

	var email string
	err := s.db.QueryRowContext(r.Context(), `SELECT email FROM accounts_pool WHERE lower(email)=lower($1)`, accountID).Scan(&email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "account email is empty"})
		return
	}

	domain := strings.TrimSpace(s.cfg.MailWorkerDomain)
	adminPassword := strings.TrimSpace(s.cfg.MailAdminPassword)
	if domain == "" || adminPassword == "" {
		writeErr(w, http.StatusBadGateway, errors.New("mail worker config missing (MAIL_WORKER_DOMAIN / MAIL_ADMIN_PASSWORD)"))
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	jwtToken, err := s.fetchMailJWTByEmail(r.Context(), client, domain, adminPassword, email)
	if err != nil && errors.Is(err, errMailAddressNotFound) {
		if createErr := s.ensureMailAddressExists(r.Context(), client, domain, adminPassword, email); createErr == nil {
			jwtToken, err = s.fetchMailJWTByEmail(r.Context(), client, domain, adminPassword, email)
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	items, err := s.fetchMailItems(r.Context(), client, domain, jwtToken)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		return mailItemCreatedTS(items[i]) > mailItemCreatedTS(items[j])
	})

	capHint := limit
	if len(items) < capHint {
		capHint = len(items)
	}
	mails := make([]map[string]any, 0, capHint)
	for i, it := range items {
		if i >= limit {
			break
		}
		id := extractMailField(it, "id", "mail_id", "message_id")
		if id == "" {
			id = fmt.Sprintf("mail-%d", i+1)
		}
		subject := extractMailField(it, "subject", "title")
		from := extractMailField(it, "from", "sender", "from_email")
		to := extractMailField(it, "to", "recipient", "to_email")
		createdAt := extractMailField(it, "created_at", "createdAt", "received_at", "updated_at", "date", "timestamp")
		if createdAt == "" {
			if ts := mailItemCreatedTS(it); ts > 0 {
				createdAt = time.Unix(ts, 0).Format(time.RFC3339)
			}
		}
		htmlBody := extractMailHTML(it)
		textBody := extractMailText(it)
		preview := compactMailText(extractMailField(it, "snippet", "preview"))
		if preview == "" {
			preview = compactMailText(textBody)
		}
		if preview == "" && htmlBody != "" {
			preview = "HTML mail"
		}
		preview = shortMailText(preview, 140)
		mails = append(mails, map[string]any{
			"id":         id,
			"subject":    subject,
			"from":       from,
			"to":         to,
			"created_at": createdAt,
			"preview":    preview,
			"otp_code":   extractOTPFromMailItem(it),
			"html":       htmlBody,
			"text":       textBody,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"account_id": accountID,
		"email":      email,
		"count":      len(mails),
		"mails":      mails,
	})
}

func extractMailField(item map[string]any, keys ...string) string {
	if item == nil {
		return ""
	}
	targets := []map[string]any{item}
	for _, nk := range []string{"data", "payload", "content", "mail"} {
		sub, _ := item[nk].(map[string]any)
		if sub != nil {
			targets = append(targets, sub)
		}
	}
	for _, obj := range targets {
		for _, k := range keys {
			if v, ok := obj[k]; ok {
				if s := stringifyMailValue(v); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func stringifyMailValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		s := strings.TrimSpace(x)
		if s == "" || s == "<nil>" {
			return ""
		}
		return s
	case []string:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			if s := strings.TrimSpace(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := []string{}
		for _, it := range x {
			if s := stringifyMailValue(it); s != "" {
				parts = append(parts, s)
			}
			if len(parts) >= 3 {
				break
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		for _, k := range []string{"address", "email", "value", "name", "text"} {
			if s := stringifyMailValue(x[k]); s != "" {
				return s
			}
		}
		return ""
	default:
		s := strings.TrimSpace(fmt.Sprintf("%v", x))
		if s == "" || s == "<nil>" {
			return ""
		}
		return s
	}
}

func extractMailHTML(item map[string]any) string {
	if s := extractMailField(item, "html", "body_html", "bodyHtml", "message_html", "raw_html", "rawHtml"); s != "" {
		return s
	}
	raw := extractMailField(item, "raw", "rawData", "rawdata", "source", "mime")
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<body") {
		return raw
	}
	return ""
}

func extractMailText(item map[string]any) string {
	return extractMailField(item, "text", "plain_text", "plainText", "body", "snippet", "preview", "subject")
}

func compactMailText(s string) string {
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\u00a0", " ")), " ")
}

func shortMailText(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

func (s *Server) fetchLatestOTPByEmail(ctx context.Context, email string, timeoutSec int) (string, error) {
	domain := strings.TrimSpace(s.cfg.MailWorkerDomain)
	adminPassword := strings.TrimSpace(s.cfg.MailAdminPassword)
	if domain == "" || adminPassword == "" {
		return "", errors.New("mail worker config missing (MAIL_WORKER_DOMAIN / MAIL_ADMIN_PASSWORD)")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	jwtToken, err := s.fetchMailJWTByEmail(ctx, client, domain, adminPassword, email)
	if err != nil && errors.Is(err, errMailAddressNotFound) {
		if createErr := s.ensureMailAddressExists(ctx, client, domain, adminPassword, email); createErr == nil {
			jwtToken, err = s.fetchMailJWTByEmail(ctx, client, domain, adminPassword, email)
		}
	}
	if err != nil {
		return "", err
	}
	if jwtToken == "" {
		return "", errors.New("mail jwt not found")
	}

	pollSec := maxInt(1, s.cfg.MailPollIntervalSeconds)
	recentFloor := time.Now().Add(-time.Duration(maxInt(30, s.cfg.MailRecentSeconds)) * time.Second).Unix()
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		items, err := s.fetchMailItems(ctx, client, domain, jwtToken)
		if err == nil && len(items) > 0 {
			sort.SliceStable(items, func(i, j int) bool {
				return mailItemCreatedTS(items[i]) > mailItemCreatedTS(items[j])
			})
			for _, it := range items {
				ts := mailItemCreatedTS(it)
				if ts > 0 && ts < recentFloor {
					continue
				}
				code := extractOTPFromMailItem(it)
				if code != "" {
					return code, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(pollSec) * time.Second):
		}
	}
	return "", errors.New("otp timeout")
}

func (s *Server) fetchMailJWTByEmail(ctx context.Context, client *http.Client, domain, adminPassword, email string) (string, error) {
	q := url.Values{}
	q.Set("query", email)
	q.Set("limit", "1")
	q.Set("offset", "0")
	u := fmt.Sprintf("https://%s/admin/address?%s", domain, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-admin-auth", adminPassword)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mail admin address query failed: %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	rows, _ := data["results"].([]any)
	if len(rows) < 1 {
		return "", errMailAddressNotFound
	}
	row0, _ := rows[0].(map[string]any)
	addressID := strings.TrimSpace(fmt.Sprintf("%v", row0["id"]))
	if addressID == "" {
		return "", errors.New("mail address id empty")
	}

	u2 := fmt.Sprintf("https://%s/admin/show_password/%s", domain, url.PathEscape(addressID))
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, u2, nil)
	if err != nil {
		return "", err
	}
	req2.Header.Set("x-admin-auth", adminPassword)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mail admin show_password failed: %d", resp2.StatusCode)
	}
	var data2 map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&data2); err != nil {
		return "", err
	}
	jwtToken := strings.TrimSpace(fmt.Sprintf("%v", data2["jwt"]))
	if jwtToken == "" {
		return "", errors.New("mail jwt empty")
	}
	return jwtToken, nil
}

func (s *Server) ensureMailAddressExists(ctx context.Context, client *http.Client, workerDomain, adminPassword, email string) error {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(email)), "@", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid email for mail address create: %s", email)
	}
	body, _ := json.Marshal(map[string]any{
		"enablePrefix": true,
		"name":         strings.TrimSpace(parts[0]),
		"domain":       strings.TrimSpace(parts[1]),
	})
	u := fmt.Sprintf("https://%s/admin/new_address", workerDomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-admin-auth", adminPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		bs, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		msg := strings.TrimSpace(string(bs))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("mail create failed: %s", msg)
	}
	return nil
}

func (s *Server) fetchMailItems(ctx context.Context, client *http.Client, domain, jwtToken string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, 20)
	for _, offset := range []int{0, 10} {
		u := fmt.Sprintf("https://%s/api/mails?limit=10&offset=%d", domain, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwtToken)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("mail fetch failed: %d", resp.StatusCode)
		}
		var data map[string]any
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		rows, _ := data["results"].([]any)
		if len(rows) == 0 {
			break
		}
		for _, x := range rows {
			obj, _ := x.(map[string]any)
			if obj != nil {
				out = append(out, obj)
			}
		}
		if len(rows) < 10 {
			break
		}
	}
	return out, nil
}

var otpRegexWithContext = regexp.MustCompile(`(?i)(?:验证码|代.?码|verification|one[- ]?time|otp|code)[^0-9]{0,32}([0-9]{6})`)
var otpRegexFallback = regexp.MustCompile(`\b([0-9]{6})\b`)

func extractOTPFromMailItem(item map[string]any) string {
	if item == nil {
		return ""
	}
	texts := make([]string, 0, 12)
	appendText := func(v any) {
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s != "" && s != "<nil>" {
			texts = append(texts, s)
		}
	}
	for _, k := range []string{"raw", "rawData", "rawdata", "source", "mime", "eml", "message", "full", "body", "html", "text", "subject", "title", "snippet", "preview"} {
		appendText(item[k])
	}
	for _, nk := range []string{"data", "payload", "content"} {
		sub, _ := item[nk].(map[string]any)
		if sub == nil {
			continue
		}
		for _, k := range []string{"raw", "rawData", "rawdata", "source", "mime", "eml", "message", "full", "body", "html", "text", "subject", "title", "snippet", "preview"} {
			appendText(sub[k])
		}
	}
	js, _ := json.Marshal(item)
	appendText(string(js))

	for _, t := range texts {
		if m := otpRegexWithContext.FindStringSubmatch(t); len(m) > 1 && m[1] != "177010" {
			return m[1]
		}
		if m := otpRegexFallback.FindStringSubmatch(t); len(m) > 1 && m[1] != "177010" {
			return m[1]
		}
	}
	return ""
}

func mailItemCreatedTS(item map[string]any) int64 {
	if item == nil {
		return 0
	}
	for _, k := range []string{"created_at", "createdAt", "received_at", "updated_at", "timestamp", "date"} {
		v, ok := item[k]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case float64:
			n := int64(x)
			for n > 1_000_000_000_000 {
				n /= 1000
			}
			if n > 0 {
				return n
			}
		case int64:
			n := x
			for n > 1_000_000_000_000 {
				n /= 1000
			}
			if n > 0 {
				return n
			}
		case string:
			s := strings.TrimSpace(x)
			if s == "" {
				continue
			}
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				for n > 1_000_000_000_000 {
					n /= 1000
				}
				if n > 0 {
					return n
				}
			}
			if ts, err := time.Parse(time.RFC3339, s); err == nil {
				return ts.Unix()
			}
			if ts, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
				return ts.Unix()
			}
		}
	}
	return 0
}

func (s *Server) handleAccountTags(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		AccountType string   `json:"account_type"`
		Tags        []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	accType := strings.TrimSpace(strings.ToLower(body.AccountType))
	if accType != "" && accType != "normal" && accType != "team" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "account_type must be normal/team"})
		return
	}
	tags := uniqueTags(body.Tags)
	if accType != "" {
		tags = uniqueTags(append(tags, accType))
	}
	var currentIdentity string
	err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(account_identity,'normal') FROM accounts_pool WHERE lower(email)=lower($1)`, accountID).Scan(&currentIdentity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	newIdentity := derivePoolIdentity(accType, tags, currentIdentity)
	_, err = s.db.ExecContext(r.Context(), `UPDATE accounts_pool SET account_identity=$1, updated_at=NOW() WHERE lower(email)=lower($2)`, newIdentity, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"account_id":       accountID,
		"tags":             poolIdentityToTags(newIdentity),
		"account_type":     poolIdentityToAccountType(newIdentity),
		"team_subscribed":  poolIdentityTeamSubscribed(newIdentity),
		"account_identity": newIdentity,
	})
}

func (s *Server) handleAccountTokenCheck(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	resp, statusCode, err := s.runTokenCheckByAccountID(r.Context(), accountID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request, accountID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	var blockedTeamID, blockedTeamName string
	err = tx.QueryRowContext(r.Context(), `
		SELECT id, COALESCE(NULLIF(team_name,''), NULLIF(name,''), id)
		FROM teams
		WHERE lower(COALESCE(owner_account_id,''))=lower($1)
		   OR lower(COALESCE(account_id,''))=lower($1)
		ORDER BY created_at ASC
		LIMIT 1
	`, accountID).Scan(&blockedTeamID, &blockedTeamName)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("account is referenced by team %s (%s), remove team binding first", blockedTeamID, blockedTeamName),
		})
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	teamIDs := []string{}
	rows, err := tx.QueryContext(r.Context(), `SELECT DISTINCT team_id FROM team_invitations WHERE lower(account_id)=lower($1)`, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for rows.Next() {
		var teamID string
		if err := rows.Scan(&teamID); err != nil {
			_ = rows.Close()
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		teamID = strings.TrimSpace(teamID)
		if teamID != "" {
			teamIDs = append(teamIDs, teamID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = rows.Close()

	inviteRes, err := tx.ExecContext(r.Context(), `DELETE FROM team_invitations WHERE lower(account_id)=lower($1)`, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	removedInvites := 0
	if n, nErr := inviteRes.RowsAffected(); nErr == nil {
		removedInvites = int(n)
	}

	res, err := tx.ExecContext(r.Context(), `DELETE FROM accounts_pool WHERE lower(email)=lower($1)`, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "account not found"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rollback = false

	for _, teamID := range teamIDs {
		_ = s.syncTeamCounters(r.Context(), teamID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"account_id":      accountID,
		"removed_invites": removedInvites,
		"affected_teams":  len(teamIDs),
	})
}

func (s *Server) runTokenCheckByAccountID(ctx context.Context, accountID string) (map[string]any, int, error) {
	var email, storedRefreshToken string
	err := s.db.QueryRowContext(ctx, `SELECT email, COALESCE(refresh_token,'') FROM accounts_pool WHERE lower(email)=lower($1)`, accountID).Scan(&email, &storedRefreshToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, http.StatusNotFound, fmt.Errorf("account not found")
		}
		return nil, http.StatusInternalServerError, err
	}

	probe, refreshErr, err := s.runAccountTokenCheck(ctx, storedRefreshToken)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	refreshTokenToSave := strings.TrimSpace(probe.RefreshToken)
	if refreshTokenToSave == "" {
		refreshTokenToSave = strings.TrimSpace(storedRefreshToken)
	}
	expiresAt := parseOptionalRFC3339(probe.ExpiresAt)
	lastRefresh := parseOptionalRFC3339(probe.LastRefresh)
	probe.CheckMethod = strings.TrimSpace(probe.CheckMethod)
	if probe.CheckMethod == "" {
		probe.CheckMethod = "refresh_token"
	}
	tokenErr := strings.TrimSpace(probe.Error)
	if probe.OK {
		tokenErr = ""
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE accounts_pool
		 SET access_token=$1,
		     refresh_token=$2,
		     id_token=$3,
		     token_expired_at=$4,
		     token_last_refresh=$5,
		     token_alive=$6,
		     token_check_method=$7,
		     token_len=$8,
		     error=$9,
		     last_token_check=NOW(),
		     updated_at=NOW()
		 WHERE lower(email)=lower($10)`,
		probe.AccessToken,
		refreshTokenToSave,
		probe.IDToken,
		expiresAt,
		lastRefresh,
		probe.OK,
		probe.CheckMethod,
		len(probe.AccessToken),
		tokenErr,
		accountID,
	)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	resp := map[string]any{
		"ok":                probe.OK,
		"account_id":        accountID,
		"email":             email,
		"access_token":      probe.AccessToken,
		"refresh_token":     refreshTokenToSave,
		"id_token":          probe.IDToken,
		"expires_at":        probe.ExpiresAt,
		"last_refresh":      probe.LastRefresh,
		"check_method":      probe.CheckMethod,
		"access_token_len":  len(probe.AccessToken),
		"refresh_token_len": len(refreshTokenToSave),
		"id_token_len":      len(probe.IDToken),
		"error":             probe.Error,
	}
	if refreshErr != nil {
		resp["refresh_error"] = refreshErr.Error()
	}
	return resp, http.StatusOK, nil
}

func (s *Server) runAccountTokenCheck(ctx context.Context, storedRefreshToken string) (*tokenProbeResult, error, error) {
	refreshToken := strings.TrimSpace(storedRefreshToken)
	if refreshToken == "" {
		refreshErr := fmt.Errorf("refresh token is empty")
		return &tokenProbeResult{
			OK:          false,
			Error:       refreshErr.Error(),
			CheckMethod: "refresh_token",
			LastRefresh: time.Now().UTC().Format(time.RFC3339),
			TokenLen:    0,
		}, refreshErr, nil
	}
	probe, refreshErr := s.runCodexRefreshFlow(ctx, refreshToken)
	if refreshErr != nil {
		return &tokenProbeResult{
			OK:          false,
			Error:       refreshErr.Error(),
			CheckMethod: "refresh_token",
			LastRefresh: time.Now().UTC().Format(time.RFC3339),
			TokenLen:    0,
		}, refreshErr, nil
	}
	probe.CheckMethod = "refresh_token"
	return probe, nil, nil
}

func (s *Server) runCodexRefreshFlow(ctx context.Context, refreshToken string) (*tokenProbeResult, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	ctx2, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	form := url.Values{
		"client_id":     {codexClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"scope":         {"openid profile email"},
	}
	req, err := http.NewRequestWithContext(ctx2, http.MethodPost, codexTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build refresh request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("refresh token failed with status %d: %s", resp.StatusCode, msg)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode refresh response failed: %w", err)
	}
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("refresh response has empty access_token")
	}
	nextRefreshToken := strings.TrimSpace(tokenResp.RefreshToken)
	if nextRefreshToken == "" {
		nextRefreshToken = rt
	}
	now := time.Now().UTC()
	expiresAt := ""
	if tokenResp.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	return &tokenProbeResult{
		OK:           true,
		AccessToken:  accessToken,
		RefreshToken: nextRefreshToken,
		IDToken:      strings.TrimSpace(tokenResp.IDToken),
		ExpiresAt:    expiresAt,
		LastRefresh:  now.Format(time.RFC3339),
		TokenLen:     len(accessToken),
	}, nil
}

func parseOptionalRFC3339(raw string) sql.NullTime {
	s := strings.TrimSpace(raw)
	if s == "" {
		return sql.NullTime{}
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return sql.NullTime{Time: ts.UTC(), Valid: true}
		}
	}
	return sql.NullTime{}
}

func (s *Server) handleTeamsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleTeamsList(w, r)
		return
	case http.MethodPost:
		s.handleTeamCreate(w, r)
		return
	default:
		writeMethodNotAllowed(w)
		return
	}
}

func (s *Server) handleTeamOwnersImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	raw := strings.TrimSpace(body.Text)
	if raw == "" {
		path := strings.TrimSpace(body.Path)
		if path == "" {
			path = "../../legacy/extension_backend/account_status.json"
		}
		bs, err := os.ReadFile(path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		raw = string(bs)
	}

	statusMap := map[string]accountStatusItem{}
	if err := json.Unmarshal([]byte(raw), &statusMap); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid account_status.json: %w", err))
		return
	}

	type ownerImported struct {
		AccountID string `json:"account_id"`
		Email     string `json:"email"`
		TeamID    string `json:"team_id"`
	}
	imported := make([]ownerImported, 0)
	createdTeams := 0
	skippedNotFound := make([]string, 0)

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	for emailRaw, rec := range statusMap {
		email := strings.ToLower(strings.TrimSpace(emailRaw))
		if email == "" || !rec.TeamSubscribed {
			continue
		}

		accountID := email
		var exists bool
		err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM accounts_pool WHERE lower(email)=lower($1))`, email).Scan(&exists)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !exists {
			skippedNotFound = append(skippedNotFound, email)
			continue
		}
		_, err = tx.ExecContext(r.Context(), `
			UPDATE accounts_pool
			SET account_identity='team_owner',
			    status='status_import',
			    updated_at=NOW()
			WHERE lower(email)=lower($1)
		`, email)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}

		teamID := ""
		err = tx.QueryRowContext(r.Context(), `SELECT id FROM teams WHERE lower(owner_account_id)=lower($1) ORDER BY created_at ASC LIMIT 1`, accountID).Scan(&teamID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				teamID = uuid.NewString()
				teamName := "Team-" + strings.Split(email, "@")[0]
				_, err = tx.ExecContext(r.Context(), `
					INSERT INTO teams (id, name, team_name, email, status, owner_account_id, max_members, current_members, account_role, created_at, updated_at)
					VALUES ($1,$2,$2,$3,'active',$4,6,0,'account-owner',NOW(),NOW())
				`, teamID, teamName, email, accountID)
				if err != nil {
					writeErr(w, http.StatusInternalServerError, err)
					return
				}
				createdTeams++
			} else {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		} else {
			_, err = tx.ExecContext(r.Context(), `
				UPDATE teams
				SET status='active',
				    email=COALESCE(NULLIF(email,''), $2),
				    team_name=COALESCE(NULLIF(team_name,''), name),
				    account_role='account-owner',
				    updated_at=NOW()
				WHERE id=$1
			`, teamID, email)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}

		imported = append(imported, ownerImported{
			AccountID: accountID,
			Email:     email,
			TeamID:    teamID,
		})
	}

	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                       true,
		"imported_count":           len(imported),
		"created_teams":            createdTeams,
		"skipped_not_found":        len(skippedNotFound),
		"skipped_not_found_emails": skippedNotFound,
		"owners":                   imported,
	})
}

func (s *Server) handleTeamCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Name           string `json:"name"`
		OwnerAccountID string `json:"owner_account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name is required"})
		return
	}
	id := uuid.NewString()
	ownerID := strings.ToLower(strings.TrimSpace(body.OwnerAccountID))
	ownerEmail := ownerID
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO teams (id, name, team_name, email, owner_account_id, status, max_members, current_members, account_role, created_at, updated_at)
		VALUES ($1,$2,$2,$3,NULLIF($4,''),'active',6,0,'account-owner',NOW(),NOW())
	`, id, name, ownerEmail, ownerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"team": map[string]any{
			"id":               id,
			"name":             name,
			"owner_account_id": strings.TrimSpace(body.OwnerAccountID),
			"status":           "active",
		},
	})
}

func (s *Server) handleTeamsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireRole(w, r, "viewer") {
		return
	}
	page := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		if x, err := strconv.Atoi(raw); err == nil && x > 0 {
			page = x
		}
	}
	perPage := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("per_page")); raw != "" {
		if x, err := strconv.Atoi(raw); err == nil && x > 0 && x <= 200 {
			perPage = x
		}
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	offset := (page - 1) * perPage

	var total int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*)
		FROM teams t
		LEFT JOIN accounts_pool a ON lower(a.email)=lower(t.owner_account_id)
		WHERE ($1='' OR t.name ILIKE '%' || $1 || '%' OR t.team_name ILIKE '%' || $1 || '%' OR COALESCE(a.email,'') ILIKE '%' || $1 || '%' OR COALESCE(t.email,'') ILIKE '%' || $1 || '%' OR COALESCE(t.account_id,'') ILIKE '%' || $1 || '%')
	`, search).Scan(&total)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT
			t.id,
			COALESCE(NULLIF(t.email,''), COALESCE(a.email,'')) AS email,
			COALESCE(t.account_id,'') AS account_id,
			COALESCE(NULLIF(t.team_name,''), t.name) AS team_name,
			GREATEST(COALESCE(t.current_members,0), COALESCE(SUM(CASE WHEN i.status IN ('accepted','joined') THEN 1 ELSE 0 END),0))::int AS current_members,
			COALESCE(t.max_members,6)::int AS max_members,
			COALESCE(t.subscription_plan,'') AS subscription_plan,
			COALESCE(TO_CHAR(t.expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS expires_at,
			COALESCE(t.status,'active') AS status,
			COALESCE(t.account_role,'') AS account_role,
			COALESCE(t.owner_account_id,'') AS owner_account_id,
			COALESCE(a.email,'') AS owner_email,
			COALESCE(SUM(CASE WHEN i.status IN ('accepted','joined') THEN 1 ELSE 0 END),0)::int AS joined_count,
			COALESCE(SUM(CASE WHEN i.status='invited' THEN 1 ELSE 0 END),0)::int AS invited_count,
			TO_CHAR(t.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at
		FROM teams t
		LEFT JOIN accounts_pool a ON lower(a.email)=lower(t.owner_account_id)
		LEFT JOIN team_invitations i ON i.team_id=t.id
		WHERE ($1='' OR t.name ILIKE '%' || $1 || '%' OR t.team_name ILIKE '%' || $1 || '%' OR COALESCE(a.email,'') ILIKE '%' || $1 || '%' OR COALESCE(t.email,'') ILIKE '%' || $1 || '%' OR COALESCE(t.account_id,'') ILIKE '%' || $1 || '%')
		GROUP BY t.id, a.email
		ORDER BY t.updated_at DESC
		LIMIT $2 OFFSET $3
	`, search, perPage, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := []teamListRow{}
	for rows.Next() {
		var rec teamListRow
		if err := rows.Scan(
			&rec.ID,
			&rec.Email,
			&rec.AccountID,
			&rec.Name,
			&rec.CurrentMembers,
			&rec.MaxMembers,
			&rec.Subscription,
			&rec.ExpiresAt,
			&rec.Status,
			&rec.AccountRole,
			&rec.OwnerAccountID,
			&rec.OwnerEmail,
			&rec.JoinedCount,
			&rec.InvitedCount,
			&rec.UpdatedAt,
		); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, rec)
	}

	totalPages := 1
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"teams": out,
		"pagination": map[string]any{
			"page":        page,
			"per_page":    perPage,
			"total":       total,
			"total_pages": totalPages,
		},
	})
}

func (s *Server) handleTeamSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/teams/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	teamID := strings.TrimSpace(parts[0])
	action := strings.TrimSpace(parts[1])
	if teamID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "team id is required"})
		return
	}
	switch action {
	case "one-click-onboard":
		s.handleTeamOneClickOnboard(w, r, teamID)
		return
	case "one-click-random-invite":
		s.handleTeamOneClickRandomInvite(w, r, teamID)
		return
	case "info":
		s.handleTeamInfo(w, r, teamID)
		return
	case "update":
		s.handleTeamUpdate(w, r, teamID)
		return
	case "delete":
		s.handleTeamDelete(w, r, teamID)
		return
	case "owner-check":
		s.handleTeamOwnerCheck(w, r, teamID)
		return
	case "members":
		if len(parts) == 3 && parts[2] == "list" {
			s.handleTeamMembersList(w, r, teamID)
			return
		}
		if len(parts) == 3 && parts[2] == "add" {
			s.handleTeamMemberAdd(w, r, teamID)
			return
		}
		if len(parts) == 4 && parts[3] == "delete" {
			memberID, _ := url.PathUnescape(strings.TrimSpace(parts[2]))
			s.handleTeamMemberDelete(w, r, teamID, strings.TrimSpace(memberID))
			return
		}
	case "invites":
		if len(parts) == 3 && parts[2] == "revoke" {
			s.handleTeamInviteRevoke(w, r, teamID)
			return
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
}

func (s *Server) handleTeamDelete(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM teams WHERE id=$1`, teamID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "team not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_id": teamID})
}

func (s *Server) handleTeamInfo(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "viewer") {
		return
	}
	var rec teamListRow
	var accessToken, refreshToken, sessionToken, clientID string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT
			t.id,
			COALESCE(NULLIF(t.email,''), COALESCE(a.email,'')) AS email,
			COALESCE(t.account_id,'') AS account_id,
			COALESCE(NULLIF(t.team_name,''), t.name) AS team_name,
			GREATEST(COALESCE(t.current_members,0), COALESCE(SUM(CASE WHEN i.status IN ('accepted','joined') THEN 1 ELSE 0 END),0))::int AS current_members,
			COALESCE(t.max_members,6)::int AS max_members,
			COALESCE(t.subscription_plan,'') AS subscription_plan,
			COALESCE(TO_CHAR(t.expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS expires_at,
			COALESCE(t.status,'active') AS status,
			COALESCE(t.account_role,'') AS account_role,
			COALESCE(t.owner_account_id,'') AS owner_account_id,
			COALESCE(a.email,'') AS owner_email,
			COALESCE(SUM(CASE WHEN i.status IN ('accepted','joined') THEN 1 ELSE 0 END),0)::int AS joined_count,
			COALESCE(SUM(CASE WHEN i.status='invited' THEN 1 ELSE 0 END),0)::int AS invited_count,
			COALESCE(t.access_token,'') AS access_token,
			COALESCE(t.refresh_token,'') AS refresh_token,
			COALESCE(t.session_token,'') AS session_token,
			COALESCE(t.client_id,'') AS client_id,
			TO_CHAR(t.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at
		FROM teams t
		LEFT JOIN accounts_pool a ON lower(a.email)=lower(t.owner_account_id)
		LEFT JOIN team_invitations i ON i.team_id=t.id
		WHERE t.id=$1
		GROUP BY t.id, a.email
	`, teamID).Scan(
		&rec.ID,
		&rec.Email,
		&rec.AccountID,
		&rec.Name,
		&rec.CurrentMembers,
		&rec.MaxMembers,
		&rec.Subscription,
		&rec.ExpiresAt,
		&rec.Status,
		&rec.AccountRole,
		&rec.OwnerAccountID,
		&rec.OwnerEmail,
		&rec.JoinedCount,
		&rec.InvitedCount,
		&accessToken,
		&refreshToken,
		&sessionToken,
		&clientID,
		&rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "team not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"team": map[string]any{
			"id":                rec.ID,
			"email":             rec.Email,
			"account_id":        rec.AccountID,
			"team_name":         rec.Name,
			"name":              rec.Name,
			"current_members":   rec.CurrentMembers,
			"max_members":       rec.MaxMembers,
			"subscription_plan": rec.Subscription,
			"expires_at":        rec.ExpiresAt,
			"status":            rec.Status,
			"account_role":      rec.AccountRole,
			"owner_account_id":  rec.OwnerAccountID,
			"owner_email":       rec.OwnerEmail,
			"joined_count":      rec.JoinedCount,
			"invited_count":     rec.InvitedCount,
			"access_token":      accessToken,
			"refresh_token":     refreshToken,
			"session_token":     sessionToken,
			"client_id":         clientID,
			"updated_at":        rec.UpdatedAt,
		},
	})
}

func (s *Server) handleTeamUpdate(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Name           string `json:"name"`
		TeamName       string `json:"team_name"`
		Status         string `json:"status"`
		OwnerAccountID string `json:"owner_account_id"`
		Email          string `json:"email"`
		AccountID      string `json:"account_id"`
		AccessToken    string `json:"access_token"`
		RefreshToken   string `json:"refresh_token"`
		SessionToken   string `json:"session_token"`
		ClientID       string `json:"client_id"`
		MaxMembers     *int   `json:"max_members"`
		Subscription   string `json:"subscription_plan"`
		ExpiresAt      string `json:"expires_at"`
		AccountRole    string `json:"account_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var currName, currTeamName, currStatus, currEmail, currAccountID, currAccessToken, currRefreshToken, currSessionToken, currClientID, currSubscription, currRole string
	var currMaxMembers int
	var currExpiresAt sql.NullTime
	var currOwner sql.NullString
	err := s.db.QueryRowContext(r.Context(), `
		SELECT name, team_name, status, owner_account_id, email, account_id, access_token, refresh_token, session_token, client_id, max_members, subscription_plan, expires_at, account_role
		FROM teams WHERE id=$1
	`, teamID).Scan(&currName, &currTeamName, &currStatus, &currOwner, &currEmail, &currAccountID, &currAccessToken, &currRefreshToken, &currSessionToken, &currClientID, &currMaxMembers, &currSubscription, &currExpiresAt, &currRole)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "team not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = currName
	}
	teamName := strings.TrimSpace(body.TeamName)
	if teamName == "" {
		teamName = currTeamName
	}
	if teamName == "" {
		teamName = name
	}
	statusVal := strings.ToLower(strings.TrimSpace(body.Status))
	if statusVal == "" {
		statusVal = currStatus
	}
	if !isTeamStatus(statusVal) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid status"})
		return
	}
	ownerID := strings.TrimSpace(body.OwnerAccountID)
	if ownerID == "" && currOwner.Valid {
		ownerID = strings.TrimSpace(currOwner.String)
	}
	ownerID = strings.ToLower(ownerID)
	email := strings.TrimSpace(body.Email)
	if email == "" {
		email = currEmail
	} else {
		email = strings.ToLower(email)
	}
	accountID := strings.TrimSpace(body.AccountID)
	if accountID == "" {
		accountID = currAccountID
	}
	accessToken := strings.TrimSpace(body.AccessToken)
	if accessToken == "" {
		accessToken = currAccessToken
	}
	refreshToken := strings.TrimSpace(body.RefreshToken)
	if refreshToken == "" {
		refreshToken = currRefreshToken
	}
	sessionToken := strings.TrimSpace(body.SessionToken)
	if sessionToken == "" {
		sessionToken = currSessionToken
	}
	clientID := strings.TrimSpace(body.ClientID)
	if clientID == "" {
		clientID = currClientID
	}
	maxMembers := currMaxMembers
	if body.MaxMembers != nil && *body.MaxMembers > 0 {
		maxMembers = *body.MaxMembers
	}
	subscription := strings.TrimSpace(body.Subscription)
	if subscription == "" {
		subscription = currSubscription
	}
	accountRole := strings.TrimSpace(body.AccountRole)
	if accountRole == "" {
		accountRole = currRole
	}
	expiresAt := currExpiresAt
	if raw := strings.TrimSpace(body.ExpiresAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			expiresAt = sql.NullTime{Time: t.UTC(), Valid: true}
		}
	}

	_, err = s.db.ExecContext(r.Context(), `
		UPDATE teams
		SET name=$1,
		    team_name=$2,
		    status=$3,
		    owner_account_id=NULLIF($4,''),
		    email=$5,
		    account_id=$6,
		    access_token=$7,
		    refresh_token=$8,
		    session_token=$9,
		    client_id=$10,
		    max_members=$11,
		    subscription_plan=$12,
		    expires_at=$13,
		    account_role=$14,
		    updated_at=NOW()
		WHERE id=$15
	`, name, teamName, statusVal, ownerID, email, accountID, accessToken, refreshToken, sessionToken, clientID, maxMembers, subscription, expiresAt, accountRole, teamID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.handleTeamInfo(w, r, teamID)
}

func isTeamStatus(v string) bool {
	switch v {
	case "active", "full", "expired", "error", "banned":
		return true
	default:
		return false
	}
}

func (s *Server) handleTeamMembersList(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "viewer") {
		return
	}
	var ownerID sql.NullString
	if err := s.db.QueryRowContext(r.Context(), `SELECT owner_account_id FROM teams WHERE id=$1`, teamID).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "team not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT
			i.account_id,
			COALESCE(a.email, i.account_id) AS email,
			i.status,
			TO_CHAR(i.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at
		FROM team_invitations i
		LEFT JOIN accounts_pool a ON lower(a.email)=lower(i.account_id)
		WHERE i.team_id=$1
		ORDER BY i.updated_at DESC
	`, teamID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	members := make([]map[string]any, 0)
	for rows.Next() {
		var accountID, email, statusVal, updatedAt string
		if err := rows.Scan(&accountID, &email, &statusVal, &updatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if statusVal == "accepted" {
			statusVal = "joined"
		}
		role := "member"
		if ownerID.Valid && strings.EqualFold(ownerID.String, accountID) {
			role = "account-owner"
		}
		members = append(members, map[string]any{
			"account_id": accountID,
			"email":      email,
			"status":     statusVal,
			"role":       role,
			"updated_at": updatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"team_id": teamID,
		"members": members,
	})
}

type teamInviteAuth struct {
	TeamID       string
	AccountID    string
	AccessToken  string
	RefreshToken string
	OwnerID      string
}

type teamInviteCandidate struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func mapInviteUpstreamStatus(status int) int {
	if status >= 400 && status < 500 {
		return status
	}
	return http.StatusBadGateway
}

func upstreamError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil {
		if detail, ok := obj["detail"].(string); ok && strings.TrimSpace(detail) != "" {
			msg = strings.TrimSpace(detail)
		}
		if errObj, ok := obj["error"].(map[string]any); ok {
			if code, ok := errObj["code"].(string); ok && strings.TrimSpace(code) != "" {
				if msg != "" {
					msg = code + ": " + msg
				} else {
					msg = code
				}
			}
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("upstream invite api status %d: %s", status, msg)
}

func (s *Server) refreshTeamInviteToken(ctx context.Context, auth *teamInviteAuth) (bool, int, error) {
	rt := strings.TrimSpace(auth.RefreshToken)
	if rt == "" {
		return false, http.StatusBadRequest, fmt.Errorf("team refresh_token is required")
	}
	probe, err := s.runCodexRefreshFlow(ctx, rt)
	if err != nil {
		return false, http.StatusBadGateway, fmt.Errorf("team token refresh failed: %w", err)
	}
	auth.AccessToken = strings.TrimSpace(probe.AccessToken)
	if nextRT := strings.TrimSpace(probe.RefreshToken); nextRT != "" {
		auth.RefreshToken = nextRT
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE teams
		SET access_token=$1,
		    refresh_token=$2,
		    updated_at=NOW()
		WHERE id=$3
	`, auth.AccessToken, auth.RefreshToken, auth.TeamID)
	if err != nil {
		return false, http.StatusInternalServerError, err
	}
	if strings.TrimSpace(auth.OwnerID) != "" {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE accounts_pool
			SET access_token=$1,
			    refresh_token=CASE WHEN $2='' THEN refresh_token ELSE $2 END,
			    token_len=$3,
			    token_alive=true,
			    token_check_method='team_invite_refresh',
			    token_last_refresh=NOW(),
			    last_token_check=NOW(),
			    error=NULL,
			    updated_at=NOW()
			WHERE lower(email)=lower($4)
		`, auth.AccessToken, auth.RefreshToken, len(auth.AccessToken), auth.OwnerID)
	}
	return true, http.StatusOK, nil
}

func (s *Server) loadTeamInviteAuth(ctx context.Context, teamID string) (*teamInviteAuth, int, error) {
	var auth teamInviteAuth
	auth.TeamID = teamID
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(account_id,''), COALESCE(access_token,''), COALESCE(refresh_token,''), COALESCE(owner_account_id,'')
		FROM teams
		WHERE id=$1
	`, teamID).Scan(&auth.AccountID, &auth.AccessToken, &auth.RefreshToken, &auth.OwnerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, http.StatusNotFound, fmt.Errorf("team not found")
		}
		return nil, http.StatusInternalServerError, err
	}
	auth.AccountID = strings.TrimSpace(auth.AccountID)
	auth.AccessToken = strings.TrimSpace(auth.AccessToken)
	auth.RefreshToken = strings.TrimSpace(auth.RefreshToken)
	auth.OwnerID = strings.TrimSpace(auth.OwnerID)
	if auth.AccountID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("team account_id is empty, cannot send real invite")
	}
	if auth.AccessToken == "" {
		if _, status, err := s.refreshTeamInviteToken(ctx, &auth); err != nil {
			return nil, status, err
		}
	}
	return &auth, http.StatusOK, nil
}

func (s *Server) callTeamInviteAPI(ctx context.Context, method string, auth *teamInviteAuth, bodyPayload map[string]any) (int, error) {
	bodyBytes, err := json.Marshal(bodyPayload)
	if err != nil {
		return 0, err
	}
	urlStr := fmt.Sprintf("https://chatgpt.com/backend-api/accounts/%s/invites", url.PathEscape(auth.AccountID))
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("chatgpt-account-id", auth.AccountID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("Origin", "https://chatgpt.com")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, upstreamError(resp.StatusCode, respBody)
	}
	return resp.StatusCode, nil
}

func (s *Server) sendTeamInvite(ctx context.Context, auth *teamInviteAuth, email string) (int, error) {
	payload := map[string]any{
		"email_addresses": []string{email},
		"role":            "standard-user",
		"resend_emails":   true,
	}
	status, err := s.callTeamInviteAPI(ctx, http.MethodPost, auth, payload)
	if err == nil {
		return http.StatusOK, nil
	}
	if status == http.StatusUnauthorized && strings.TrimSpace(auth.RefreshToken) != "" {
		if _, refreshStatus, refreshErr := s.refreshTeamInviteToken(ctx, auth); refreshErr != nil {
			return refreshStatus, refreshErr
		}
		return s.callTeamInviteAPI(ctx, http.MethodPost, auth, payload)
	}
	return status, err
}

func (s *Server) revokeTeamInvite(ctx context.Context, auth *teamInviteAuth, email string) (int, error) {
	payload := map[string]any{"email_address": email}
	status, err := s.callTeamInviteAPI(ctx, http.MethodDelete, auth, payload)
	if err == nil {
		return http.StatusOK, nil
	}
	if status == http.StatusUnauthorized && strings.TrimSpace(auth.RefreshToken) != "" {
		if _, refreshStatus, refreshErr := s.refreshTeamInviteToken(ctx, auth); refreshErr != nil {
			return refreshStatus, refreshErr
		}
		return s.callTeamInviteAPI(ctx, http.MethodDelete, auth, payload)
	}
	return status, err
}

func (s *Server) pickTeamInviteCandidates(ctx context.Context, teamID string, count int, random bool) ([]teamInviteCandidate, error) {
	orderBy := "a.created_at ASC"
	if random {
		orderBy = "RANDOM()"
	}
	query := fmt.Sprintf(`
		SELECT a.email, a.email
		FROM accounts_pool a
		WHERE lower(coalesce(a.account_identity,'normal')) in ('normal','plus')
		  AND NOT EXISTS (
			SELECT 1 FROM team_invitations i
			WHERE i.team_id=$1 AND lower(i.account_id)=lower(a.email) AND i.status IN ('invited','accepted','joined')
		  )
		ORDER BY %s
		LIMIT $2
	`, orderBy)
	rows, err := s.db.QueryContext(ctx, query, teamID, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	selected := make([]teamInviteCandidate, 0, count)
	for rows.Next() {
		var rec teamInviteCandidate
		if err := rows.Scan(&rec.ID, &rec.Email); err != nil {
			return nil, err
		}
		selected = append(selected, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *Server) upsertInvitedMember(ctx context.Context, teamID, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO team_invitations (id, team_id, account_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,'invited',NOW(),NOW())
		ON CONFLICT (team_id, account_id)
		DO UPDATE SET status='invited', updated_at=NOW()
	`, uuid.NewString(), teamID, accountID)
	return err
}

func (s *Server) handleTeamMemberAdd(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "email is required"})
		return
	}

	auth, statusCode, err := s.loadTeamInviteAuth(r.Context(), teamID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	if upstreamStatus, err := s.sendTeamInvite(r.Context(), auth, email); err != nil {
		writeErr(w, mapInviteUpstreamStatus(upstreamStatus), err)
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	accountID := email
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO accounts_pool (email, account_identity, status, updated_at)
		VALUES ($1,'normal','ui_member_add',NOW())
		ON CONFLICT (email) DO UPDATE
		SET updated_at=NOW()
	`, email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO team_invitations (id, team_id, account_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,'invited',NOW(),NOW())
		ON CONFLICT (team_id, account_id)
		DO UPDATE SET status='invited', updated_at=NOW()
	`, uuid.NewString(), teamID, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = tx.ExecContext(r.Context(), `UPDATE teams SET updated_at=NOW() WHERE id=$1`, teamID)
	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.syncTeamCounters(r.Context(), teamID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"team_id":    teamID,
		"account_id": accountID,
		"email":      email,
		"status":     "invited",
	})
}

func (s *Server) handleTeamMemberDelete(w http.ResponseWriter, r *http.Request, teamID, accountID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	if accountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "account id is required"})
		return
	}
	res, err := s.db.ExecContext(r.Context(), `DELETE FROM team_invitations WHERE team_id=$1 AND account_id=$2`, teamID, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "member not found"})
		return
	}
	_ = s.syncTeamCounters(r.Context(), teamID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_id": teamID, "account_id": accountID})
}

func (s *Server) handleTeamInviteRevoke(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "email is required"})
		return
	}
	auth, statusCode, err := s.loadTeamInviteAuth(r.Context(), teamID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	if upstreamStatus, err := s.revokeTeamInvite(r.Context(), auth, email); err != nil {
		writeErr(w, mapInviteUpstreamStatus(upstreamStatus), err)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `
		UPDATE team_invitations i
		SET status='revoked', updated_at=NOW()
		WHERE i.team_id=$1 AND lower(i.account_id)=lower($2) AND i.status='invited'
	`, teamID, email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.syncTeamCounters(r.Context(), teamID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_id": teamID, "email": email, "status": "revoked"})
}

func (s *Server) handleTeamOwnerCheck(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var ownerID string
	err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(owner_account_id,'') FROM teams WHERE id=$1`, teamID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "team not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if ownerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "team has no owner account"})
		return
	}
	lookupID := strings.ToLower(strings.TrimSpace(ownerID))
	resp, statusCode, err := s.runTokenCheckByAccountID(r.Context(), lookupID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	_ = s.syncTeamCounters(r.Context(), teamID)
	resp["team_id"] = teamID
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTeamOneClickRandomInvite(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Count int `json:"count"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	count := body.Count
	if count <= 0 {
		count = 1
	}
	if count > 20 {
		count = 20
	}

	auth, statusCode, err := s.loadTeamInviteAuth(r.Context(), teamID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	selected, err := s.pickTeamInviteCandidates(r.Context(), teamID, count, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	success := make([]teamInviteCandidate, 0, len(selected))
	failed := make([]map[string]any, 0)
	for _, candidate := range selected {
		status, err := s.sendTeamInvite(r.Context(), auth, strings.ToLower(strings.TrimSpace(candidate.Email)))
		if err != nil {
			failed = append(failed, map[string]any{
				"id":     candidate.ID,
				"email":  candidate.Email,
				"status": status,
				"error":  err.Error(),
			})
			continue
		}
		if err := s.upsertInvitedMember(r.Context(), teamID, candidate.ID); err != nil {
			failed = append(failed, map[string]any{
				"id":     candidate.ID,
				"email":  candidate.Email,
				"status": http.StatusInternalServerError,
				"error":  fmt.Sprintf("invite sent but local persist failed: %v", err),
			})
			continue
		}
		success = append(success, candidate)
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE teams SET updated_at=NOW() WHERE id=$1`, teamID)
	_ = s.syncTeamCounters(r.Context(), teamID)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"team_id":           teamID,
		"selected_count":    len(success),
		"selected_accounts": success,
		"failed_count":      len(failed),
		"failed_accounts":   failed,
		"mode":              "random_pool_real_invite",
	})
}

func (s *Server) handleTeamOneClickOnboard(w http.ResponseWriter, r *http.Request, teamID string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !s.requireRole(w, r, "operator") {
		return
	}
	var body struct {
		Count int `json:"count"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	count := body.Count
	if count <= 0 {
		count = 1
	}
	if count > 100 {
		count = 100
	}

	auth, statusCode, err := s.loadTeamInviteAuth(r.Context(), teamID)
	if err != nil {
		writeErr(w, statusCode, err)
		return
	}
	selected, err := s.pickTeamInviteCandidates(r.Context(), teamID, count, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	success := make([]teamInviteCandidate, 0, len(selected))
	failed := make([]map[string]any, 0)
	for _, candidate := range selected {
		status, err := s.sendTeamInvite(r.Context(), auth, strings.ToLower(strings.TrimSpace(candidate.Email)))
		if err != nil {
			failed = append(failed, map[string]any{
				"id":     candidate.ID,
				"email":  candidate.Email,
				"status": status,
				"error":  err.Error(),
			})
			continue
		}
		if err := s.upsertInvitedMember(r.Context(), teamID, candidate.ID); err != nil {
			failed = append(failed, map[string]any{
				"id":     candidate.ID,
				"email":  candidate.Email,
				"status": http.StatusInternalServerError,
				"error":  fmt.Sprintf("invite sent but local persist failed: %v", err),
			})
			continue
		}
		success = append(success, candidate)
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE teams SET updated_at=NOW() WHERE id=$1`, teamID)
	_ = s.syncTeamCounters(r.Context(), teamID)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                               true,
		"team_id":                          teamID,
		"selected_count":                   len(success),
		"selected_accounts":                success,
		"failed_count":                     len(failed),
		"failed_accounts":                  failed,
		"accept_invite_protocol_supported": false,
		"accept_invite_note":               "当前仅支持发送邀请，尚未实现被邀请账号的协议化 accept invite。",
	})
}

func (s *Server) syncTeamCounters(ctx context.Context, teamID string) error {
	var joined int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status IN ('accepted','joined') THEN 1 ELSE 0 END),0)::int
		FROM team_invitations
		WHERE team_id=$1
	`, teamID).Scan(&joined); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE teams
		SET current_members=$2,
		    status = CASE
		             WHEN status IN ('active','full') THEN
		               CASE WHEN $2 >= COALESCE(NULLIF(max_members,0),6) THEN 'full' ELSE 'active' END
		             ELSE status
		             END,
		    updated_at=NOW()
		WHERE id=$1
	`, teamID, joined)
	return err
}

func (s *Server) teamExists(ctx context.Context, teamID string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM teams WHERE id=$1`, teamID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Server) handleUIRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui" && r.URL.Path != "/mvp_invite_workspace.html" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": err.Error(),
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

var tagRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

func uniqueTags(tags []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, t := range tags {
		tag := strings.ToLower(strings.TrimSpace(t))
		if tag == "" || !tagRe.MatchString(tag) {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func splitTags(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return []string{}
	}
	parts := strings.Split(csv, ",")
	return uniqueTags(parts)
}

func boolToPoolIdentity(teamSubscribed bool) string {
	if teamSubscribed {
		return "team_member"
	}
	return "normal"
}

func poolIdentityToAccountType(identity string) string {
	switch strings.ToLower(strings.TrimSpace(identity)) {
	case "team_owner", "team_member":
		return "team"
	default:
		return "normal"
	}
}

func poolIdentityTeamSubscribed(identity string) bool {
	switch strings.ToLower(strings.TrimSpace(identity)) {
	case "team_owner", "team_member":
		return true
	default:
		return false
	}
}

func poolIdentityToTags(identity string) []string {
	idn := strings.ToLower(strings.TrimSpace(identity))
	if idn == "" {
		idn = "normal"
	}
	base := poolIdentityToAccountType(idn)
	return uniqueTags([]string{base, idn})
}

func derivePoolIdentity(accountType string, tags []string, current string) string {
	cur := strings.ToLower(strings.TrimSpace(current))
	if cur == "" {
		cur = "normal"
	}
	accType := strings.ToLower(strings.TrimSpace(accountType))
	tagSet := map[string]struct{}{}
	for _, t := range tags {
		tag := strings.ToLower(strings.TrimSpace(t))
		if tag != "" {
			tagSet[tag] = struct{}{}
		}
	}
	if _, ok := tagSet["team_owner"]; ok {
		return "team_owner"
	}
	if _, ok := tagSet["team_member"]; ok {
		return "team_member"
	}
	if _, ok := tagSet["team_subscribed"]; ok {
		return "team_member"
	}
	if _, ok := tagSet["team"]; ok {
		return "team_member"
	}
	if _, ok := tagSet["plus"]; ok {
		return "plus"
	}
	if _, ok := tagSet["normal"]; ok {
		return "normal"
	}
	switch accType {
	case "team":
		if cur == "team_owner" {
			return "team_owner"
		}
		return "team_member"
	case "normal":
		if cur == "plus" {
			return "plus"
		}
		return "normal"
	default:
		return cur
	}
}

func envOrDefault(k, d string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	return v
}

func envIntOrDefault(k string, d int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return d
	}
	return n
}

func resolveDatabaseDSNFromEnv() (string, string) {
	candidates := []string{
		"DATABASE_URL",
		"SUPABASE_DB_URL",
		"SUPABASE_DATABASE_URL",
	}
	for _, key := range candidates {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		return normalizeDSNForSupabase(raw), key
	}
	return "", ""
}

func dsnHost(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

func normalizeDSNForSupabase(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" {
		return dsn
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	isSupabase := strings.Contains(host, "supabase.co") || strings.Contains(host, "supabase.com")
	if !isSupabase {
		return dsn
	}

	q := u.Query()
	// Supabase Postgres 通常要求 TLS。
	if strings.TrimSpace(q.Get("sslmode")) == "" {
		q.Set("sslmode", "require")
	}
	// Supabase Pooler（PgBouncer）在一些驱动场景下建议 simple protocol。
	if strings.Contains(host, ".pooler.supabase.com") && strings.TrimSpace(q.Get("default_query_exec_mode")) == "" {
		q.Set("default_query_exec_mode", "simple_protocol")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func configureDBPool(db *sql.DB) {
	if db == nil {
		return
	}
	maxOpen := envIntOrDefault("DB_MAX_OPEN_CONNS", 10)
	maxIdle := envIntOrDefault("DB_MAX_IDLE_CONNS", 5)
	maxLifetime := envIntOrDefault("DB_CONN_MAX_LIFETIME_SECONDS", 1800)
	maxIdleTime := envIntOrDefault("DB_CONN_MAX_IDLE_TIME_SECONDS", 600)

	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}
	if maxIdle >= 0 {
		db.SetMaxIdleConns(maxIdle)
	}
	if maxLifetime > 0 {
		db.SetConnMaxLifetime(time.Duration(maxLifetime) * time.Second)
	}
	if maxIdleTime > 0 {
		db.SetConnMaxIdleTime(time.Duration(maxIdleTime) * time.Second)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
