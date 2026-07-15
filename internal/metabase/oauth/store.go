package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record — персистируемое состояние OAuth-клиента и токена.
//
// Кроме самих токенов храним привязку к issuer/resource/token_endpoint/scopes/
// client_id/client_mode: при смене любого из этих параметров сохранённый токен
// использовать нельзя (он выдан для другого контекста) — нужен повторный вход.
type Record struct {
	Issuer        string    `json:"issuer"`
	Resource      string    `json:"resource"`
	TokenEndpoint string    `json:"token_endpoint"`
	ClientID      string    `json:"client_id"`
	RedirectURI   string    `json:"redirect_uri"`
	Scopes        []string  `json:"scopes"`
	ClientMode    string    `json:"client_mode"` // "public" | "confidential"
	RefreshToken  string    `json:"refresh_token"`
	AccessToken   string    `json:"access_token"`
	Expiry        time.Time `json:"expiry"`
}

// SaveRecord атомарно пишет запись в path с правами 0600 (каталог 0700).
//
// Порядок: mkdir 0700 → temp-файл 0600 в ТОМ ЖЕ каталоге → fsync-close →
// os.Rename (атомарно на одной ФС) → принудительный chmod 0600.
// Обычный os.WriteFile не «чинит» уже широкий mode существующего файла —
// поэтому rename+chmod.
func SaveRecord(path string, rec *Record) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("oauth: mkdir token dir: %w", err)
	}
	// На случай, если каталог уже существовал с более широкими правами.
	_ = os.Chmod(dir, 0o700)

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("oauth: marshal token record: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("oauth: create temp token file: %w", err)
	}
	tmpName := tmp.Name()
	// Гарантируем удаление temp, если что-то пошло не так до rename.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("oauth: chmod temp token file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("oauth: write temp token file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("oauth: sync temp token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("oauth: close temp token file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("oauth: rename token file into place: %w", err)
	}
	committed = true
	// Принудительно сужаем права финального файла (rename сохраняет права temp,
	// но подстрахуемся на случай пред-существующего файла/umask-эффектов).
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("oauth: chmod token file: %w", err)
	}
	return nil
}

// LoadRecord читает запись из path. Требует regular-file; при слишком широких
// правах — сужает до 0600 (best-effort «чинить/предупреждать»).
func LoadRecord(path string) (*Record, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("oauth: stat token file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("oauth: token file %q is not a regular file", path)
	}
	// Если group/other имеют любые права — сужаем.
	if fi.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(path, 0o600)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("oauth: read token file: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("oauth: decode token file %q: %w", path, err)
	}
	return &rec, nil
}
