package lxd

import (
	"context"
	"fmt"

	"github.com/canonical/lxd/shared/api"
)

var _ ImageService = (*FakeInstanceServer)(nil)

// ImageStore holds the in-memory image + alias state backing the fake
// ImageService implementation.
type ImageStore struct {
	Images  map[string]*api.Image // key: fingerprint
	Aliases map[string]string     // alias name -> fingerprint
	Fetches []ImageFetchRecord    // every CopyRemoteImage request (assertion)
}

// ImageFetchRecord records one CopyRemoteImage invocation on the fake.
type ImageFetchRecord struct {
	RemoteURL  string
	Alias      string
	Type       string
	LocalAlias string
}

// AddImages initializes the image backing stores (called by
// NewFakeInstanceServer).
func (f *FakeInstanceServer) AddImages() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AddImagesLocked()
}

// AddImagesLocked initializes the image stores; callers must hold f.mu.
func (f *FakeInstanceServer) AddImagesLocked() {
	if f.Images == nil {
		f.Images = &ImageStore{
			Images:  make(map[string]*api.Image),
			Aliases: make(map[string]string),
		}
	}
}

func (f *FakeInstanceServer) GetImages() ([]api.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.Image
	if f.Images == nil {
		return out, nil
	}
	for _, img := range f.Images.Images {
		out = append(out, *img)
	}
	return out, nil
}

func (f *FakeInstanceServer) GetImageAliases() ([]api.ImageAliasesEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetImageAliasesFunc != nil {
		return f.GetImageAliasesFunc()
	}
	var out []api.ImageAliasesEntry
	if f.Images == nil {
		return out, nil
	}
	for name, fp := range f.Images.Aliases {
		out = append(out, api.ImageAliasesEntry{Name: name, Target: fp})
	}
	return out, nil
}

// CopyRemoteImage records the pull request and seeds the canonical local alias
// so idempotency tests can assert a second apply no-ops. When the alias
// already exists it returns LXD's "Alias already exists" error, which the
// executor treats as a success/no-op (§7.7) — exactly how the real daemon
// behaves under a concurrent fetch.
func (f *FakeInstanceServer) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AddImagesLocked()
	if f.CopyRemoteImageFunc != nil {
		return f.CopyRemoteImageFunc(ctx, remoteURL, alias, imageType, localAlias)
	}
	f.Images.Fetches = append(f.Images.Fetches, ImageFetchRecord{
		RemoteURL:  remoteURL,
		Alias:      alias,
		Type:       imageType,
		LocalAlias: localAlias,
	})
	if _, exists := f.Images.Aliases[localAlias]; exists {
		//nolint:staticcheck // ST1005: intentionally mirrors LXD's real daemon message.
		return fmt.Errorf("Alias already exists: %s", localAlias)
	}
	fp := "fake-fingerprint-" + localAlias
	f.Images.Images[fp] = &api.Image{
		Fingerprint: fp,
		Type:        imageType,
		Aliases:     []api.ImageAlias{{Name: localAlias}},
	}
	f.Images.Aliases[localAlias] = fp
	return nil
}
