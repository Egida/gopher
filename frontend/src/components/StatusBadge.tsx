interface Props { status: string; className?: string }

export default function StatusBadge({ status, className = '' }: Props) {
  const s = status.toLowerCase()
  let color = 'bg-gray-400'
  // Distinct hue per confidence level, not shades of one hue — active
  // (confirmed responding) and connected (up but didn't answer the probe)
  // read too similarly as two shades of green at a glance. Idle = amber,
  // offline = red.
  if (s === 'active') color = 'bg-green-500'
  else if (s === 'connected') color = 'bg-blue-500'
  else if (s === 'idle') color = 'bg-amber-400'
  else if (s === 'pending' || s === 'connecting') color = 'bg-yellow-500'
  else if (s === 'inactive' || s === 'disconnected') color = 'bg-gray-400'
  else if (s === 'offline' || s === 'failed' || s === 'error' || s.startsWith('config-error')) color = 'bg-red-500'

  return (
    <span className={`inline-flex items-center gap-1.5 text-sm ${className}`}>
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
      <span className="capitalize">{status}</span>
    </span>
  )
}
