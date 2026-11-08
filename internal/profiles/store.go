package profiles

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const keySize = 32
const maxProfileBytes = 8 << 20

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// Profile contains subscription source material. It is encrypted at rest by Store.
type Profile struct {
	ID              string   `json:"id"`
	SourceID        string   `json:"source_id"`
	Enabled         bool     `json:"enabled"`
	Listen          string   `json:"listen,omitempty"`
	Port            uint16   `json:"port"`
	Strategy        string   `json:"strategy,omitempty"`
	OutboundInterface string `json:"outbound_interface,omitempty"`
	Links           []string `json:"links,omitempty"`
	Subscription    string   `json:"subscription,omitempty"`
	SubscriptionURL string   `json:"subscription_url,omitempty"`
}

type Metadata struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Enabled  bool   `json:"enabled"`
	Listen   string `json:"listen,omitempty"`
	Port     uint16 `json:"port"`
	Strategy string `json:"strategy,omitempty"`
	OutboundInterface string `json:"outbound_interface,omitempty"`
	Input    string `json:"input"`
}

type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

func Validate(p Profile) error {
	if !profileIDPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid profile id")
	}
	if !sourceIDPattern.MatchString(p.SourceID) {
		return fmt.Errorf("invalid source id")
	}
	inputs := 0
	if len(p.Links) > 0 {
		inputs++
	}
	if p.Subscription != "" {
		inputs++
	}
	if p.SubscriptionURL != "" {
		inputs++
	}
	if inputs != 1 {
		return fmt.Errorf("profile must contain exactly one of links, subscription, or subscription_url")
	}
	if len(p.Links) > 10000 {
		return fmt.Errorf("too many profile links")
	}
	for _, link := range p.Links {
		if strings.TrimSpace(link) == "" || len(link) > 65536 {
			return fmt.Errorf("invalid profile link")
		}
	}
	if len(p.Subscription) > maxProfileBytes {
		return fmt.Errorf("subscription body exceeds 8 MiB limit")
	}
	if len(p.SubscriptionURL) > 8192 {
		return fmt.Errorf("subscription URL is too long")
	}
	return nil
}

func (s *Store) ensureDir() error {
	if s.dir == "" {
		return fmt.Errorf("profile store directory is not configured")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("profile store directory must be private and not a symlink")
	}
	return nil
}

func (s *Store) loadKey(create bool) ([]byte, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "profiles.key")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		key := make([]byte, keySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				return s.loadKey(false)
			}
			return nil, err
		}
		if _, err = f.Write(key); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if d, e := os.Open(s.dir); e == nil {
			_ = d.Sync()
			_ = d.Close()
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("profile encryption key must be a private regular file")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("invalid profile encryption key length")
	}
	return key, nil
}

func (s *Store) Save(p Profile) error {
	if err := Validate(p); err != nil {
		return err
	}
	key, err := s.loadKey(true)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(p.ID))
	blob := append([]byte{1}, nonce...)
	blob = append(blob, sealed...)
	if len(blob) > maxProfileBytes {
		return fmt.Errorf("encrypted profile exceeds 8 MiB limit")
	}
	tmp, err := os.CreateTemp(s.dir, ".profile-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, p.ID+".profile")); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *Store) Get(id string) (Profile, error) {
	if !profileIDPattern.MatchString(id) {
		return Profile{}, fmt.Errorf("invalid profile id")
	}
	key, err := s.loadKey(false)
	if err != nil {
		return Profile{}, err
	}
	path := filepath.Join(s.dir, id+".profile")
	info, err := os.Lstat(path)
	if err != nil {
		return Profile{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Profile{}, fmt.Errorf("profile file must be a private regular file")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Profile{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Profile{}, err
	}
	if len(blob) < 1+gcm.NonceSize()+gcm.Overhead() || blob[0] != 1 || len(blob) > maxProfileBytes {
		return Profile{}, fmt.Errorf("invalid encrypted profile file")
	}
	nonce := blob[1 : 1+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, blob[1+gcm.NonceSize():], []byte(id))
	if err != nil {
		return Profile{}, fmt.Errorf("cannot decrypt profile")
	}
	var p Profile
	if err := json.Unmarshal(plain, &p); err != nil {
		return Profile{}, fmt.Errorf("invalid decrypted profile")
	}
	if p.ID != id {
		return Profile{}, fmt.Errorf("profile identity mismatch")
	}
	if err := Validate(p); err != nil {
		return Profile{}, fmt.Errorf("invalid stored profile")
	}
	return p, nil
}

func (s *Store) List() ([]Metadata, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Metadata, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".profile") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".profile")
		p, err := s.Get(id)
		if err != nil {
			return nil, fmt.Errorf("load stored profile %s: %w", id, err)
		}
		input := "links"
		if p.Subscription != "" {
			input = "inline"
		}
		if p.SubscriptionURL != "" {
			input = "url"
		}
		out = append(out, Metadata{ID: p.ID, SourceID: p.SourceID, Enabled: p.Enabled, Listen: p.Listen, Port: p.Port, Strategy: p.Strategy, OutboundInterface: p.OutboundInterface, Input: input})
	}
	return out, nil
}

func (s *Store) ListProfiles() ([]Profile, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".profile") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".profile")
		p, err := s.Get(id)
		if err != nil {
			return nil, fmt.Errorf("load stored profile %s: %w", id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) Delete(id string) error {
	if !profileIDPattern.MatchString(id) {
		return fmt.Errorf("invalid profile id")
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	path := filepath.Join(s.dir, id+".profile")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("profile file must be a private regular file")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
