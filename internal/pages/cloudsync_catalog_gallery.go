package pages

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// set_catalog_image v2 (ut-docs#7101, ADR-0218): an item's gallery or one
// variant's gallery. Position 1 of an item gallery is the thumbnail
// (items/<id>/thumb.png, the file every reader and LAN sync already knows);
// the others are g-<sha16>.png beside it, a variant's v-<variant>-<n>-<sha16>.png.
// Every image is fetched and decoded BEFORE anything is written, so a failed
// fetch leaves the gallery as it was. Files are written, then the rows replaced in
// one transaction, then superseded files removed (best effort).

const galleryAssetPrefix = "/public/assets/items/"

func itemAssetDir(itemID string) string { return paths.Data("public", "assets", "items", itemID) }

func galleryFileName(prefix string, n int, sum string) string {
	return fmt.Sprintf("%s%d-%s.png", prefix, n, sum[:16])
}

// removeGalleryFiles deletes files of one item whose name starts with prefix
// and are not in keep (best effort: a leftover file is harmless, a missing one is not).
func removeGalleryFiles(itemID, prefix string, keep map[string]bool) {
	entries, err := os.ReadDir(itemAssetDir(itemID))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || keep[e.Name()] {
			continue
		}
		_ = os.Remove(filepath.Join(itemAssetDir(itemID), e.Name()))
	}
}

func itemHasGallery(ctx context.Context, repo *data.CatalogRepo, img cloudsync.CatalogImage) bool {
	g, err := repo.ItemGallery(ctx, img.ID)
	return err != nil || len(g) > 0
}

func sameGallery(a, b []data.GalleryImage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Alt != b[i].Alt {
			return false
		}
	}
	return true
}

func cloudSetItemGallery(ctx context.Context, d *common.Deps, repo *data.CatalogRepo, img cloudsync.CatalogImage, name string) (string, error) {
	if !safeCategoryID(img.VariantID) && img.VariantID != "" {
		return "", fmt.Errorf("variant %s is not on this till", img.VariantID)
	}
	audit := func(action string, payload map[string]any) {
		auditCloudDirective(ctx, d, "item", img.ID, action, payload)
		d.NudgeLink(fleetlink.ScopeAdmin)
	}
	variant := img.VariantID != ""
	if img.Clear {
		msg := fmt.Sprintf("images removed from item %s", name)
		var err error
		if variant {
			msg = fmt.Sprintf("images removed from a variant of item %s", name)
			err = repo.ReplaceVariantGallery(ctx, img.ID, img.VariantID, nil)
			if errors.Is(err, data.ErrItemNotFound) {
				return "", fmt.Errorf("variant %s is not on item %s", img.VariantID, name)
			}
			removeGalleryFiles(img.ID, "v-"+img.VariantID+"-", nil)
		} else if err = repo.ReplaceItemGallery(ctx, img.ID, nil); err == nil {
			removeGalleryFiles(img.ID, "g-", nil)
		}
		if err != nil {
			return "", err
		}
		audit("cloud_catalog_image_cleared", nil)
		return msg, nil
	}

	// 1. fetch and decode everything first
	pics := make([]image.Image, len(img.Images))
	for i, ref := range img.Images {
		if ref.Fetch == nil {
			return "", errors.New("this till cannot download images right now; save the images again to retry")
		}
		raw, err := ref.Fetch(ctx)
		if err != nil {
			return "", err
		}
		if pics[i], err = imaging.PrepareThumb(raw); err != nil {
			if errors.Is(err, imaging.ErrTooManyPixels) {
				return "", fmt.Errorf("image %d from the cloud has too many pixels for this till", i+1)
			}
			return "", fmt.Errorf("image %d from the cloud could not be read as a PNG or JPEG", i+1)
		}
	}
	// 2. files and the rows they will have
	rows := make([]data.GalleryImage, len(img.Images))
	keep := map[string]bool{}
	prefix := "g-"
	if variant {
		prefix = "v-" + img.VariantID + "-"
	}
	beforeSHA := ""
	for i, ref := range img.Images {
		file, url := "", ""
		if i == 0 && !variant {
			file = filepath.Join(itemAssetDir(img.ID), "thumb.png")
			url = catalog.ItemThumbURL(img.ID)
			beforeSHA = cloudsync.ServedImageSHA256(url)
		} else {
			fn := galleryFileName(prefix, i+1, ref.SHA256)
			keep[fn] = true
			file, url = filepath.Join(itemAssetDir(img.ID), fn), galleryAssetPrefix+img.ID+"/"+fn
		}
		if err := imaging.WriteThumbPNG(pics[i], file); err != nil {
			return "", err
		}
		rows[i] = data.GalleryImage{Path: url, Alt: ref.Alt}
	}
	// 3. rows in one transaction
	var before []data.GalleryImage
	var err error
	if variant {
		before, _ = repo.VariantGallery(ctx, img.ID, img.VariantID)
		err = repo.ReplaceVariantGallery(ctx, img.ID, img.VariantID, rows)
		if errors.Is(err, data.ErrItemNotFound) {
			removeGalleryFiles(img.ID, prefix, keepOnly(before))
			return "", fmt.Errorf("variant %s is not on item %s", img.VariantID, name)
		}
	} else {
		before, _ = repo.ItemGallery(ctx, img.ID)
		err = repo.ReplaceItemGallery(ctx, img.ID, rows)
	}
	if err != nil {
		return "", err
	}
	removeGalleryFiles(img.ID, prefix, keep)
	if !variant {
		catalog.RefreshBasketPhoto(ctx, d, repo, img.ID)
	}
	changed := !sameGallery(before, rows)
	if !variant && cloudsync.ServedImageSHA256(catalog.ItemThumbURL(img.ID)) != beforeSHA {
		changed = true
	}
	if changed || variant && len(before) == 0 {
		audit("cloud_catalog_image_set", map[string]any{"sha256": img.SHA256, "images": len(img.Images), "variant_id": img.VariantID})
	}
	if variant {
		return fmt.Sprintf("images set on a variant of item %s", name), nil
	}
	return fmt.Sprintf("images set on item %s", name), nil
}

// keepOnly turns existing rows' file names into a keep set (used when a write
// is refused after new files landed: only the previous files survive).
func keepOnly(rows []data.GalleryImage) map[string]bool {
	k := map[string]bool{}
	for _, r := range rows {
		k[filepath.Base(r.Path)] = true
	}
	return k
}
