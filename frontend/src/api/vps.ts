import client from './client'
import type { VPSConfig, ApiResponse } from '../types'

export const vpsApi = {
  get: () => client.get<ApiResponse<VPSConfig>>('/vps/').then(r => r.data),
  generateToken: (tunnelPort?: number, sshKeyID?: string, publicSSH?: boolean) => client.post<ApiResponse<{ token: string; bootstrap_command: string; expires_at: string }>>('/bootstrap/token', {
    ...(tunnelPort ? { tunnel_port: tunnelPort } : {}),
    ...(sshKeyID ? { ssh_key_id: sshKeyID } : {}),
    ...(publicSSH ? { public_ssh: true } : {}),
  }).then(r => r.data),
}
