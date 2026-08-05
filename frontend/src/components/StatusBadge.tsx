interface Props { status: string; className?: string }

export default function StatusBadge({ status, className = '' }: Props) {
  const s = status.toLowerCase()
  let color = 'bg-gray-400'
  // Active (confirmed responding) and connected (up but didn't answer the
  // probe) are both "healthy" greens — connected gets a lighter tint of the
  // same green rather than a different hue (teal read as "off", not just
  // "different"). This also matters for machines, which have no "active"
  // tier at all — connected IS their best state, so it needs to read as
  // healthy-green, not as a notch-below-good in an unrelated color.
  // Idle = amber, offline = red.
  if (s === 'active') color = 'bg-green-500'
  else if (s === 'connected') color = 'bg-green-300'
  else if (s === 'idle') color = 'bg-amber-400'
  else if (s === 'pending' || s === 'connecting' || s === 'provisioning') color = 'bg-yellow-500'
  else if (s === 'inactive' || s === 'disconnected') color = 'bg-gray-400'
  else if (s === 'offline' || s === 'failed' || s === 'error' || s.startsWith('config-error')) color = 'bg-red-500'

  return (
    <span className={`inline-flex items-center gap-1.5 text-sm ${className}`}>
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
      <span className="capitalize">{status}</span>
    </span>
  )
}
