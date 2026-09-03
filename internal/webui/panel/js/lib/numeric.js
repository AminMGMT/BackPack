/* Which form fields the panel sends as numbers.
 *
 * Everything else goes as typed. Guessing from the value — "it is all digits,
 * so it is a number" — is what broke both forms: a tunnel port of 19997 was
 * posted as 19997 rather than "19997", and Go, which declares TunnelPort a
 * string, refused the whole request. These are the int fields of NewTunnel,
 * NewDirectTunnel, FineTune and TunnelLimits; internal/webui pins this list
 * against those structs.
 */
export const NUMERIC = new Set([
  'paths', 'greKey', 'maxConnections', 'bandwidthMbps',
  'limits.maxConnections', 'limits.bandwidthMbps',
  'tune.keepAlive', 'tune.heartbeat', 'tune.mss', 'tune.channelSize',
  'tune.connectionPool', 'tune.muxCon', 'tune.muxVersion', 'tune.muxFrameSize',
  'tune.muxRecvBuffer', 'tune.muxStreamBuffer',
  'tune.kcpMTU', 'tune.kcpInterval', 'tune.kcpSndWnd', 'tune.kcpRcvWnd',
  'tune.kcpDataShards', 'tune.kcpParityShards',
]);
