package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/iconid"
)

// MaxGalleryImages is the cap per item or variant (ADR-0218, ut-docs#7101).
const MaxGalleryImages = 10

// MaxGalleryAltBytes caps one alt text (ADR-0218: 512 characters).
const MaxGalleryAltBytes = 2048

// GalleryImage is one picture of an item or variant gallery. Position is
// 1-based (1 = the primary, which is the item's thumbnail); it is filled on
// read and ignored on write (the slice order is the order).
type GalleryImage struct {
	Position int
	Path     string
	Alt      string
}

func checkGallery(imgs []GalleryImage) error {
	if len(imgs) > MaxGalleryImages {
		return fmt.Errorf("a gallery holds at most %d images", MaxGalleryImages)
	}
	for i, g := range imgs {
		if strings.TrimSpace(g.Path) == "" {
			return fmt.Errorf("gallery image %d has no path", i+1)
		}
		if len(g.Alt) > MaxGalleryAltBytes {
			return fmt.Errorf("gallery image %d alt text is too long", i+1)
		}
	}
	return nil
}

// ReplaceItemGallery replaces the item's whole gallery in one transaction:
// imgs[0] becomes the thumbnail (item_images role 'thumbnail', so every
// existing reader keeps working and shows the primary), the rest role
// 'gallery' at positions 2... An empty list clears the picture and gallery
// (the icon id is kept, as ClearItemPhoto does). Unknown item: ErrItemNotFound.
func (r *CatalogRepo) ReplaceItemGallery(ctx context.Context, itemID string, imgs []GalleryImage) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	if err := checkGallery(imgs); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("item gallery: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var icon, thumb string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(icon,''),
	        COALESCE((SELECT path FROM item_images WHERE item_id = items.id AND role = 'thumbnail' LIMIT 1), '')
	        FROM items WHERE id = ?`, itemID).Scan(&icon, &thumb)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrItemNotFound
	}
	if err != nil {
		return fmt.Errorf("item gallery: read: %w", err)
	}
	keep := icon
	if keep == "" {
		keep = iconid.IDForAssetPath(thumb)
	}
	primary := ""
	if len(imgs) > 0 {
		primary = strings.TrimSpace(imgs[0].Path)
		if iconid.IDForAssetPath(primary) != "" {
			return fmt.Errorf("%q is a library tile, not a photo", primary)
		}
	}
	if err := setItemPictureExec(ctx, tx, itemID, primary, keep); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_images WHERE item_id = ? AND role = 'gallery'`, itemID); err != nil {
		return fmt.Errorf("item gallery: clear: %w", err)
	}
	if len(imgs) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE item_images SET alt = ?, sort_order = 1 WHERE item_id = ? AND role = 'thumbnail'`, imgs[0].Alt, itemID); err != nil {
			return fmt.Errorf("item gallery: primary alt: %w", err)
		}
	}
	for i := 1; i < len(imgs); i++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO item_images (id, item_id, path, role, sort_order, alt) VALUES (?, ?, ?, 'gallery', ?, ?)`,
			uuid.NewString(), itemID, strings.TrimSpace(imgs[i].Path), i+1, imgs[i].Alt); err != nil {
			return fmt.Errorf("item gallery: insert %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("item gallery: commit: %w", err)
	}
	return nil
}

// ItemGallery returns the item's pictures in order: the thumbnail first
// (Position 1), then the gallery rows. No pictures: an empty slice.
func (r *CatalogRepo) ItemGallery(ctx context.Context, itemID string) ([]GalleryImage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT path, alt, role, sort_order FROM item_images
		 WHERE item_id = ? AND role IN ('thumbnail','gallery')
		 ORDER BY CASE role WHEN 'thumbnail' THEN 0 ELSE 1 END, sort_order, id LIMIT ?`, itemID, MaxGalleryImages)
	if err != nil {
		return nil, fmt.Errorf("item gallery: read: %w", err)
	}
	defer rows.Close()
	var out []GalleryImage
	for rows.Next() {
		var g GalleryImage
		var role string
		var order int
		if err := rows.Scan(&g.Path, &g.Alt, &role, &order); err != nil {
			return nil, err
		}
		g.Position = len(out) + 1
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceVariantGallery replaces one variant's pictures (item_images role
// 'variant', position 1 = the variant's primary). The item's own thumbnail
// and gallery are untouched. An empty list clears the variant's pictures.
func (r *CatalogRepo) ReplaceVariantGallery(ctx context.Context, itemID, variantID string, imgs []GalleryImage) error {
	if itemID == "" || variantID == "" {
		return errors.New("itemID and variantID required")
	}
	if err := checkGallery(imgs); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("variant gallery: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_variants WHERE id = ? AND item_id = ?`, variantID, itemID).Scan(&n); err != nil {
		return fmt.Errorf("variant gallery: read: %w", err)
	}
	if n == 0 {
		return ErrItemNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_images WHERE item_id = ? AND role = 'variant' AND variant_id = ?`, itemID, variantID); err != nil {
		return fmt.Errorf("variant gallery: clear: %w", err)
	}
	for i, g := range imgs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO item_images (id, item_id, path, role, sort_order, alt, variant_id) VALUES (?, ?, ?, 'variant', ?, ?, ?)`,
			uuid.NewString(), itemID, strings.TrimSpace(g.Path), i+1, g.Alt, variantID); err != nil {
			return fmt.Errorf("variant gallery: insert %d: %w", i+1, err)
		}
	}
	return tx.Commit()
}

// VariantGallery returns one variant's pictures in order.
func (r *CatalogRepo) VariantGallery(ctx context.Context, itemID, variantID string) ([]GalleryImage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT path, alt FROM item_images WHERE item_id = ? AND role = 'variant' AND variant_id = ?
		 ORDER BY sort_order, id LIMIT ?`, itemID, variantID, MaxGalleryImages)
	if err != nil {
		return nil, fmt.Errorf("variant gallery: read: %w", err)
	}
	defer rows.Close()
	var out []GalleryImage
	for rows.Next() {
		var g GalleryImage
		if err := rows.Scan(&g.Path, &g.Alt); err != nil {
			return nil, err
		}
		g.Position = len(out) + 1
		out = append(out, g)
	}
	return out, rows.Err()
}
