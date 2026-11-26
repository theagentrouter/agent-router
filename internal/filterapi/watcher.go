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

	"golang.org/x/sync/singleflight"

	"github.com/envoyproxy/ai-gateway/internal/version"
)

var errBundlePartNotFound = errors.New("bundle part not found")

// ConfigReceiver is an interface that can receive *Config updates.
// This is mostly for decoupling and testing purposes.
type ConfigReceiver interface {
	// LoadConfig updates the configuration.
	LoadConfig(ctx context.Context, config *Config) error
}

// ReloadableConfigReceiver is a [ConfigReceiver] that can ask for the configuration to be read again before the
// next periodic load, for example when a request names a backend that the loaded configuration does not have yet.
// [StartConfigBundleWatcher] gives it the reload function before it starts watching.
type ReloadableConfigReceiver interface {
	ConfigReceiver
	// SetConfigReloader receives the function that reads the configuration again. The function returns when that
	// read is done, or with the context's error if the context ends first. The read does not run under that context.
	SetConfigReloader(reload func(context.Context) error)
}

// bundleLoadKey is the only key of bundleConfigWatcher.loads.
const bundleLoadKey = "bundle"

type bundleConfigWatcher struct {
	// ctx is the context the watcher was started with. Loads after the first run under it, including the ones a
	// receiver asks for, so a request's context never becomes the context of a load.
	ctx  context.Context
	tick time.Duration
	// loads runs one load at a time: a tick or a reload that comes while a load is running waits for that load.
	loads      singleflight.Group
	lastMod    time.Time
	path       string
	rcv        ConfigReceiver
	l          *slog.Logger
	versionStr string
}

// StartConfigBundleWatcher starts a watcher for the sharded bundle directory.
func StartConfigBundleWatcher(ctx context.Context, bundlePath string, rcv ConfigReceiver, l *slog.Logger, tick time.Duration) error {
	cw := &bundleConfigWatcher{ctx: ctx, tick: tick, rcv: rcv, l: l, path: bundlePath, versionStr: version.Parse()}

	if err := cw.loadConfig(ctx); err != nil {
		// The initial load of the bundle may fail because of race condition
		// when the bundle is being created. We will retry on the next tick.
		if !errors.Is(err, ErrBundleChecksumMismatch) && !errors.Is(err, errBundlePartNotFound) {
			return fmt.Errorf("failed to load initial bundled config: %w", err)
		}
		l.Warn("failed to load initial bundled config; will retry on next watch tick", slog.String("error", err.Error()))
	}
	if r, ok := rcv.(ReloadableConfigReceiver); ok {
		r.SetConfigReloader(cw.reload)
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
			if _, err, _ := cw.loads.Do(bundleLoadKey, cw.load); err != nil {
				cw.l.Error("failed to update bundled config", slog.String("error", err.Error()))
			}
		}
	}
}

// load reads the bundle as a tick does: under the watcher's context, for at most one tick.
func (cw *bundleConfigWatcher) load() (any, error) {
	ctx, cancel := context.WithTimeout(cw.ctx, cw.tick)
	defer cancel()
	return nil, cw.loadConfig(ctx)
}

// reload reads the bundle now instead of at the next tick. If a load is already running, it waits for that one.
// ctx bounds only the wait: if it ends first, reload returns its error and the load still runs to the end.
func (cw *bundleConfigWatcher) reload(ctx context.Context) error {
	ch := cw.loads.DoChan(bundleLoadKey, cw.load)
	select {
	case res := <-ch:
		return res.Err
	case <-ctx.Done():
		return ctx.Err()
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
