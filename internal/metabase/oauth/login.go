package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

// ClientAuth описывает, как клиент аутентифицируется на token endpoint.
// Public: секрет пуст, client_id идёт в параметрах (AuthStyleInParams).
// Confidential: секрет задан, способ (_post/_basic) отражён в AuthStyle.
type ClientAuth struct {
	ClientID     string
	ClientSecret string // "" → public client
	Mode         string // "public" | "confidential"
	AuthStyle    oauth2.AuthStyle
}

// browserOpener — точка подмены в тестах (реальный браузер там не нужен).
var browserOpener = openBrowser

// BindCallback валидирует loopback-адрес, поднимает listener и собирает
// redirect_uri из фактического (возможно, эфемерного) порта.
//
// Это ОТДЕЛЬНЫЙ первый шаг: redirect_uri нужен и для DCR, и для authorize/
// exchange, и все три обязаны использовать один и тот же URI.
func BindCallback(redirectAddr string) (net.Listener, string, error) {
	if err := checkLoopback(redirectAddr); err != nil {
		return nil, "", fmt.Errorf("oauth: redirect addr: %w", err)
	}
	lis, err := net.Listen("tcp", redirectAddr)
	if err != nil {
		return nil, "", fmt.Errorf("oauth: bind callback listener: %w", err)
	}
	tcpAddr, ok := lis.Addr().(*net.TCPAddr)
	if !ok {
		_ = lis.Close()
		return nil, "", fmt.Errorf("oauth: unexpected listener addr type %T", lis.Addr())
	}
	if !tcpAddr.IP.IsLoopback() {
		_ = lis.Close()
		return nil, "", fmt.Errorf("oauth: bound to non-loopback address %s", tcpAddr.String())
	}
	host := net.JoinHostPort(tcpAddr.IP.String(), strconv.Itoa(tcpAddr.Port))
	redirectURI := "http://" + host + "/callback"
	return lis, redirectURI, nil
}

// InteractiveLogin проводит Authorization Code + PKCE flow: печатает ссылку
// авторизации в stderr, best-effort открывает браузер, ждёт колбэк на lis,
// меняет code на полный токен (access + refresh + expiry).
//
// ctx контролирует таймаут ожидания входа (обычно WithTimeout(LOGIN_TIMEOUT)).
// Для сетевых вызовов oauth2 берёт http.Client из ctx (oauth2.HTTPClient),
// поэтому вызывающий должен положить туда bounded-клиент.
func InteractiveLogin(
	ctx context.Context,
	meta *Meta,
	clientAuth ClientAuth,
	lis net.Listener,
	redirectURI string,
	scopes []string,
	resource string,
	log *slog.Logger,
) (*oauth2.Token, error) {
	conf := &oauth2.Config{
		ClientID:     clientAuth.ClientID,
		ClientSecret: clientAuth.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   meta.AuthorizeEP,
			TokenURL:  meta.TokenEP,
			AuthStyle: clientAuth.AuthStyle,
		},
		RedirectURL: redirectURI,
		Scopes:      scopes,
	}

	state, err := randToken()
	if err != nil {
		return nil, fmt.Errorf("oauth: generate state: %w", err)
	}
	verifier := oauth2.GenerateVerifier()
	authURL := conf.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", resource),
	)

	// Путь колбэка берём из redirectURI, чтобы точно совпасть с редиректом AS.
	cbPath := "/callback"
	if u, err := url.Parse(redirectURI); err == nil && u.Path != "" {
		cbPath = u.Path
	}

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	// Неблокирующая отправка: дубликат колбэка (повтор браузера) не должен
	// подвесить горутину-хендлер на переполненном канале.
	send := func(r result) {
		select {
		case resCh <- r:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cbPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			writePage(w, "Authentication failed. You can close this window.")
			send(result{err: fmt.Errorf("oauth: authorization error %q: %s", e, desc)})
			return
		}
		if got := q.Get("state"); got != state {
			writePage(w, "Authentication failed (state mismatch). You can close this window.")
			send(result{err: fmt.Errorf("oauth: state mismatch (possible CSRF)")})
			return
		}
		code := q.Get("code")
		if code == "" {
			writePage(w, "Authentication failed (no code). You can close this window.")
			send(result{err: fmt.Errorf("oauth: authorization response missing code")})
			return
		}
		writePage(w, "Authentication successful. You can close this window and return to the terminal.")
		send(result{code: code})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	// Гарантированно гасим сервер и слушатель во всех ветках.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "\nmetabase-mcp: open this URL to authorize:\n\n    %s\n\n", authURL)
	log.Info("oauth: waiting for interactive login", slog.String("redirect_uri", redirectURI))
	browserOpener(authURL)

	var code string
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("oauth: login timed out or cancelled: %w", ctx.Err())
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	}

	tok, err := conf.Exchange(ctx, code,
		oauth2.VerifierOption(verifier),
		oauth2.SetAuthURLParam("resource", resource),
	)
	if err != nil {
		return nil, fmt.Errorf("oauth: code exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("oauth: authorization server returned no refresh_token; " +
			"non-interactive refresh is impossible (check that the client/scope allows offline access)")
	}
	return tok, nil
}

// randToken возвращает 32 байта энтропии в base64url — для state.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// checkLoopback валидирует, что host:port указывает на loopback/localhost.
func checkLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("expected host:port: %w", err)
	}
	if port == "" {
		return fmt.Errorf("port is required")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("host %q is not an IP or localhost", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("host %q is not a loopback address", host)
	}
	return nil
}

func writePage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><html><body><p>%s</p></body></html>", msg)
}

// openBrowser best-effort открывает URL в системном браузере. Ошибки игнорируем:
// пользователь всегда может открыть ссылку из stderr руками.
func openBrowser(rawURL string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{rawURL}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		cmd, args = "xdg-open", []string{rawURL}
	}
	_ = exec.Command(cmd, args...).Start()
}
