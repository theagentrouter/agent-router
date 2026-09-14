// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/version"
)

var errBundlePartNotFound = errors.New("bundle part not found")

// ConfigReceiver is an interface that can receive *Config updates.
// This is mostly for decoupling and testing purposes.
type ConfigReceiver interface {
	// LoadConfig updates the configuration.
	LoadConfig(ctx context.Context, config *Config) error
}

type bundleConfigWatcher struct {
	lastMod    time.Time
	path       string
	rcv        ConfigReceiver
	l          *slog.Logger
	versionStr string
}

// StartConfigBundleWatcher starts a watcher for the sharded bundle directory.
func StartConfigBundleWatcher(ctx context.Context, bundlePath string, rcv ConfigReceiver, l *slog.Logger, tick time.Duration) error {
	cw := &bundleConfigWatcher{rcv: rcv, l: l, path: bundlePath, versionStr: version.Parse()}

	if err := cw.loadConfig(ctx); err != nil {
		// The initial load of the bundle may fail because of race condition
		// when the bundle is being created. We will retry on the next tick.
		if !errors.Is(err, ErrBundleChecksumMismatch) && !errors.Is(err, errBundlePartNotFound) {
			return fmt.Errorf("failed to load initial bundled config: %w", err)
		}
		l.Warn("failed to load initial bundled config; will retry on next watch tick", slog.String("error", err.Error()))
	}

	l.Info("start watching the config bundle", slog.String("path", bundlePath), slog.String("interval", tick.String()))
	go cw.watch(ctx, tick)
	return nil
}

func (cw *bundleConfigWatcher) watch(ctx context.Context, tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cw.l.Info("stop watching the config bundle", slog.String("path", cw.path))
			return
		case <-ticker.C:
			perTickCtx, cancel := context.WithTimeout(ctx, tick)
			if err := cw.loadConfig(perTickCtx); err != nil {
				cw.l.Error("failed to update bundled config", slog.String("error", err.Error()))
			}
			cancel()
		}
	}
}

func (cw *bundleConfigWatcher) loadConfig(ctx context.Context) error {
	indexPath := filepath.Join(cw.path, ConfigBundleIndexFileName)
	indexRaw, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	indexStat, err := os.Stat(indexPath)
	if err != nil {
		return err
	}
	index, err := UnmarshalConfigBundleIndex(indexRaw)
	if err != nil {
		return err
	}

	maxMod := indexStat.ModTime()
	for i := 0; i < len(index.Parts); i++ {
		partPath := filepath.Join(cw.path, filepath.FromSlash(index.Parts[i].Path))
		partStat, statErr := os.Stat(partPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("%w %q: %w", errBundlePartNotFound, index.Parts[i].Path, statErr)
			}
			return statErr
		}
		if partStat.ModTime().After(maxMod) {
			maxMod = partStat.ModTime()
		}
	}
	if maxMod.Sub(cw.lastMod) <= 0 {
		return nil
	}

	cw.l.Info("loading a new bundled config", slog.String("path", cw.path))
	cfg, err := ReassembleBundleConfig(index, func(part ConfigBundlePart) ([]byte, error) {
		partPath := filepath.Join(cw.path, filepath.FromSlash(part.Path))
		raw, readErr := os.ReadFile(partPath)
		if readErr != nil && errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("%w %q: %w", errBundlePartNotFound, part.Path, readErr)
		}
		return raw, readErr
	})
	if err != nil {
		return err
	}

	if cfg.Version != cw.versionStr {
		return fmt.Errorf(`config version mismatch: expected %q, got %q. Likely in the middle of rolling update`,
			cw.versionStr, cfg.Version)
	}

	if err = cw.rcv.LoadConfig(ctx, cfg); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cw.lastMod = maxMod
	return nil
}
