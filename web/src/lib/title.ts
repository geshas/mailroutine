import { useEffect } from 'react'

/** Sets the browser tab title for the current page. */
export function usePageTitle(title: string) {
  useEffect(() => {
    document.title = `${title} — MailRoutine`
  }, [title])
}
