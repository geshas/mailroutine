/**
 * Shows a toast from a mount-time effect. Child effects run before the root <Toaster/> has
 * subscribed on a fresh page load, and sonner does not replay toasts fired before that, so the
 * message would be lost (for example right after the Google redirect). Deferring one tick fixes it.
 */
export function toastAfterMount(show: () => void) {
  setTimeout(show, 0)
}
