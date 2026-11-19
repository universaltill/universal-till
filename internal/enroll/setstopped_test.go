package enroll

import "context"

// setStoppedForTest flips the stop switch; production writes the shop-wide key
// through the settings handler (saveShopSettings).
func SetStopped(ctx context.Context, kv Settings, stopped bool) error {
	v := "false"
	if stopped {
		v = "true"
	}
	if err := kv.Set(ctx, KeyStopped, v); err != nil {
		return err
	}
	if !stopped {
		KickAutoRegister()
	}
	return nil
}
