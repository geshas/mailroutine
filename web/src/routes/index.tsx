import { createFileRoute, redirect } from '@tanstack/react-router'
import { api } from '@/lib/api/client'

export const Route = createFileRoute('/')({
  beforeLoad: async () => {
    const s = await api.session().catch(() => null)
    throw redirect({ to: s ? '/dashboard' : '/login' })
  },
})
