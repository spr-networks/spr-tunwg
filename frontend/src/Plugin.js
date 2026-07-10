import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  api,
  useAlert,
  timeAgo,
  Page,
  ListHeader,
  Card,
  SectionHeader,
  StatTile,
  StatusDot,
  Toggle,
  TextField,
  ModalForm,
  ModalConfirm,
  Loading,
  EmptyState,
  Badge,
  BadgeText,
  Button,
  ButtonText,
  HStack,
  VStack,
  Text
} from '@spr-networks/plugin-ui'

const PLUGIN_BASE = `/plugins/${api.pluginURI() || 'spr-tunwg'}`

const copyText = (text, alert) => {
  const done = () => alert.success('Copied to clipboard')
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard
      .writeText(text)
      .then(done)
      .catch(() => alert.info(text))
  } else {
    alert.info(text)
  }
}

const emptyForward = {
  Name: '',
  LocalURL: '',
  Key: '',
  Auth: '',
  Relay: false,
  Enabled: true
}

// --- Client-side validation, mirroring the backend rules so users see the
// --- constraint next to the field instead of a rejected request.

const NAME_RE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/
const KEY_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/

const validateNameField = (name, forwards) => {
  if (!name) return null
  if (!NAME_RE.test(name)) {
    return 'Use 1-32 lowercase letters, digits or dashes, starting and ending with a letter or digit'
  }
  if (forwards.some((f) => f.Name === name)) {
    return `A forward named ${name} already exists`
  }
  return null
}

const parseIPv4 = (host) => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host)
  if (!m) return null
  const octets = m.slice(1).map(Number)
  return octets.every((n) => n <= 255) ? octets : null
}

const validateLocalURLField = (raw) => {
  if (!raw) return null
  let u
  try {
    u = new URL(raw)
  } catch {
    return 'Enter a full URL, like http://192.168.2.50:8123'
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    return 'The target must use http:// or https://'
  }
  if (u.username || u.password) {
    return 'Remove the credentials from the URL'
  }
  if ((u.pathname !== '' && u.pathname !== '/') || u.search || u.hash) {
    return 'Remove the path — tunwg forwards the whole host'
  }
  const host = u.hostname
  if (host.startsWith('[')) {
    const v6 = host.slice(1, -1).toLowerCase()
    if (v6 === '::1') {
      return 'Loopback is not reachable from the plugin container — use the device LAN IP'
    }
    if (!v6.startsWith('fc') && !v6.startsWith('fd')) {
      return 'IPv6 targets must be private ULA addresses (fd00::/8)'
    }
    return null
  }
  const octets = parseIPv4(host)
  if (!octets) {
    return 'Use the device LAN IP address, not a hostname'
  }
  const [a, b] = octets
  if (a === 127) {
    return 'Loopback is not reachable from the plugin container — use the device LAN IP'
  }
  const isPrivate =
    a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31)
  if (!isPrivate) {
    return 'Only private LAN addresses can be published: 192.168.x.x, 10.x.x.x or 172.16-31.x.x'
  }
  return null
}

const validateKeyField = (key) => {
  if (!key) return null
  if (!KEY_RE.test(key)) {
    return "Use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit"
  }
  return null
}

const validateAuthField = (auth) => {
  if (!auth) return null
  const idx = auth.indexOf(':')
  if (idx < 1 || idx === auth.length - 1) {
    return 'Use htpasswd format user:hash — generate one with: htpasswd -nbB user pass'
  }
  return null
}

// Calm, persistent exposure notice: one strip, muted copy, no shouting.
const ExposureNotice = () => (
  <Card p="$4">
    <HStack space="md" alignItems="center">
      <StatusDot warn size={8} />
      <Text
        flex={1}
        size="xs"
        color="$muted600"
        lineHeight="$sm"
        sx={{ _dark: { color: '$muted300' } }}
      >
        Forwards are public. Anyone with the URL can reach the target service
        through the relay, and new hostnames appear in public certificate
        transparency logs. Add basic auth to anything not meant for everyone.
      </Text>
    </HStack>
  </Card>
)

const RowBadge = ({ children }) => (
  <Badge action="muted" variant="outline" borderRadius="$full" size="sm">
    <BadgeText>{children}</BadgeText>
  </Badge>
)

const ForwardRow = ({ item, toggleBusy, onToggle, onDelete, onCopy }) => {
  const st = item.Status || {}
  const online = !!st.Running && !!st.PublicURL
  const warn = !!item.Enabled && !online
  const publicURL = st.PublicURL || ''
  const started = st.Running ? timeAgo(st.StartedAt) : null

  const meta = []
  if (st.Restarts > 0) {
    meta.push(`${st.Restarts} restart${st.Restarts === 1 ? '' : 's'}`)
  }
  if (started) {
    meta.push(`started ${started}`)
  }

  let stateText = null
  if (!item.Enabled) {
    stateText = 'Disabled'
  } else if (!publicURL) {
    stateText = st.LastError
      ? `Reconnecting — ${st.LastError}`
      : 'Connecting…'
  }

  return (
    <HStack
      space="md"
      alignItems="center"
      flexWrap="wrap"
      py="$3"
      borderBottomWidth={1}
      borderColor="$borderColorCardLight"
      sx={{ _dark: { borderColor: '$borderColorCardDark' } }}
    >
      <StatusDot online={online} warn={warn} />
      <VStack flex={1} minWidth={240} space="xs">
        <HStack space="sm" alignItems="center" flexWrap="wrap">
          <Text size="sm" bold>
            {item.Name}
          </Text>
          {item.AuthConfigured ? <RowBadge>basic auth</RowBadge> : null}
          {item.Relay ? <RowBadge>https relay</RowBadge> : null}
        </HStack>
        <HStack space="sm" alignItems="center" flexWrap="wrap">
          <Text size="xs" fontFamily="$mono" color="$muted500">
            {item.LocalURL}
          </Text>
          <Text size="xs" color="$muted400">
            →
          </Text>
          {publicURL ? (
            <HStack space="xs" alignItems="center">
              <Text size="xs" fontFamily="$mono">
                {publicURL}
              </Text>
              <Button size="xs" variant="link" onPress={() => onCopy(publicURL)}>
                <ButtonText>Copy</ButtonText>
              </Button>
            </HStack>
          ) : (
            <Text size="xs" color="$muted500">
              {stateText}
            </Text>
          )}
        </HStack>
        {meta.length ? (
          <Text size="2xs" color="$muted400">
            {meta.join(' · ')}
          </Text>
        ) : null}
      </VStack>
      <HStack space="md" alignItems="center">
        <Toggle
          value={!!item.Enabled}
          disabled={toggleBusy}
          onPress={() => onToggle(item)}
          label={`Enable forward ${item.Name}`}
        />
        <Button
          size="xs"
          variant="outline"
          action="secondary"
          onPress={() => onDelete(item)}
        >
          <ButtonText>Delete</ButtonText>
        </Button>
      </HStack>
    </HStack>
  )
}

export default function Plugin() {
  const alert = useAlert()
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(null)
  const [status, setStatus] = useState(null)
  const [forwards, setForwards] = useState([])
  const [config, setConfig] = useState(null)

  // Add-forward modal: form step, then an explicit publish confirmation.
  const [showAdd, setShowAdd] = useState(false)
  const [addStep, setAddStep] = useState('form') // 'form' | 'confirm'
  const [newForward, setNewForward] = useState(emptyForward)
  const [adding, setAdding] = useState(false)

  const [deleteTarget, setDeleteTarget] = useState(null)
  const [togglePending, setTogglePending] = useState(null)

  // Relay settings
  const [apiDomain, setApiDomain] = useState('')
  const [authToken, setAuthToken] = useState('')
  const [replaceToken, setReplaceToken] = useState(false)
  const [saving, setSaving] = useState(false)
  const [showRemoveToken, setShowRemoveToken] = useState(false)

  const timerRef = useRef(null)
  const configLoadedRef = useRef(false)

  const refresh = useCallback((withConfig) => {
    let calls = [
      api.get(`${PLUGIN_BASE}/status`),
      api.get(`${PLUGIN_BASE}/forwards`)
    ]
    if (withConfig) {
      calls.push(api.get(`${PLUGIN_BASE}/config`))
    }
    return Promise.all(calls)
      .then(([st, fwds, cfg]) => {
        setStatus(st)
        setForwards(fwds || [])
        setLoadError(null)
        if (cfg) {
          configLoadedRef.current = true
          setConfig(cfg)
          setApiDomain(cfg.APIDomain || '')
        }
      })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    refresh(true).catch((err) => setLoadError(err))
    timerRef.current = setInterval(() => {
      // Re-fetch config too if the first (config-bearing) load never succeeded.
      refresh(!configLoadedRef.current).catch(() => {})
    }, 5000)
    return () => clearInterval(timerRef.current)
  }, [refresh])

  const openAdd = () => {
    setAddStep('form')
    setShowAdd(true)
  }

  const closeAdd = () => {
    setShowAdd(false)
    setAddStep('form')
  }

  const submitAdd = () => {
    const body = {
      Name: newForward.Name.trim(),
      LocalURL: newForward.LocalURL.trim(),
      Key: newForward.Key.trim(),
      Auth: newForward.Auth.trim(),
      Relay: !!newForward.Relay,
      Enabled: !!newForward.Enabled
    }
    setAdding(true)
    api
      .post(`${PLUGIN_BASE}/forwards`, body)
      .then(() => {
        closeAdd()
        setNewForward(emptyForward)
        alert.success(`Forward ${body.Name} added — waiting for its public URL`)
        return refresh(false)
      })
      .catch((err) => {
        setAddStep('form')
        alert.error('Failed to add forward', err)
      })
      .finally(() => setAdding(false))
  }

  const toggleForward = (item) => {
    setTogglePending(item.Name)
    api
      .post(`${PLUGIN_BASE}/forwards/${item.Name}/toggle`, {})
      .then(() => refresh(false))
      .catch((err) =>
        alert.error(
          `Failed to ${item.Enabled ? 'disable' : 'enable'} ${item.Name}`,
          err
        )
      )
      .finally(() => setTogglePending(null))
  }

  const deleteForward = () => {
    const item = deleteTarget
    setDeleteTarget(null)
    if (!item) return
    api
      .delete(`${PLUGIN_BASE}/forwards/${item.Name}`)
      .then(() => {
        alert.success(`Forward ${item.Name} deleted`)
        return refresh(false)
      })
      .catch((err) => alert.error('Failed to delete forward', err))
  }

  const saveRelay = () => {
    setSaving(true)
    api
      .put(`${PLUGIN_BASE}/config`, {
        APIDomain: apiDomain.trim(),
        AuthToken: authToken.trim()
      })
      .then((cfg) => {
        setConfig(cfg)
        setAuthToken('')
        setReplaceToken(false)
        alert.success('Relay settings saved — tunnels are restarting')
        return refresh(false)
      })
      .catch((err) => alert.error('Failed to save relay settings', err))
      .finally(() => setSaving(false))
  }

  const removeToken = () => {
    api
      .put(`${PLUGIN_BASE}/config`, {
        APIDomain: config?.APIDomain || '',
        ClearAuthToken: true
      })
      .then((cfg) => {
        setConfig(cfg)
        setReplaceToken(false)
        alert.success('Relay auth token removed — tunnels are restarting')
        return refresh(false)
      })
      .catch((err) => alert.error('Failed to remove token', err))
  }

  if (loading) {
    return (
      <Page>
        <Loading text="Loading tunwg forwards..." />
      </Page>
    )
  }

  const headerDescription =
    'Expose local services with public HTTPS URLs over outbound WireGuard tunnels'

  if (loadError && !status) {
    return (
      <Page>
        <ListHeader title="Tunwg" description={headerDescription} mark="tw" />
        <Card>
          <EmptyState
            title="Can't reach the plugin backend"
            description="The tunwg plugin container may be stopped or still starting. Retry in a moment."
          >
            <Button
              size="sm"
              onPress={() => {
                setLoading(true)
                refresh(true).catch((err) => setLoadError(err))
              }}
            >
              <ButtonText>Retry</ButtonText>
            </Button>
          </EmptyState>
        </Card>
      </Page>
    )
  }

  const total = status?.ForwardsTotal || 0
  const enabled = status?.ForwardsEnabled || 0
  const running = status?.ForwardsRunning || 0
  const authCount = forwards.filter((f) => f.AuthConfigured).length

  const stateWord =
    total === 0
      ? 'Ready'
      : enabled === 0
      ? 'Paused'
      : running >= enabled
      ? 'Publishing'
      : running > 0
      ? 'Degraded'
      : 'Offline'
  const stateAction =
    total === 0
      ? 'info'
      : enabled === 0
      ? 'muted'
      : running >= enabled
      ? 'success'
      : running > 0
      ? 'warning'
      : 'error'

  // Add-forward form validation (computed each render, cheap).
  const draftName = newForward.Name.trim()
  const draftURL = newForward.LocalURL.trim()
  const nameError = validateNameField(draftName, forwards)
  const urlError = validateLocalURLField(draftURL)
  const keyError = validateKeyField(newForward.Key.trim())
  const authError = validateAuthField(newForward.Auth.trim())
  const formValid =
    !!draftName && !!draftURL && !nameError && !urlError && !keyError && !authError

  const relayDomain = status?.APIDomain || 'l.tunwg.com'
  const relayDirty =
    apiDomain.trim() !== (config?.APIDomain || '') || authToken.trim() !== ''

  return (
    <Page>
      <ListHeader
        title="Tunwg"
        description={headerDescription}
        mark="tw"
        status={stateWord}
        statusAction={stateAction}
      >
        <Button size="sm" onPress={openAdd}>
          <ButtonText>Add forward</ButtonText>
        </Button>
      </ListHeader>

      <Card>
        <SectionHeader
          title="Overview"
          right={
            <StatusDot
              online={total === 0 || (enabled > 0 && running >= enabled)}
              warn={enabled > 0 && running < enabled}
            />
          }
        />
        <HStack flexWrap="wrap" gap="$2">
          <StatTile
            label="Forwards"
            value={total === 0 ? 'None yet' : `${running} of ${total} up`}
          />
          <StatTile
            label="Basic auth"
            value={total === 0 ? '—' : `${authCount} of ${total}`}
          />
          <StatTile label="Relay" value={relayDomain} mono />
          <StatTile label="tunwg" value={status?.TunwgVersion || '—'} mono />
        </HStack>
      </Card>

      <ExposureNotice />

      <Card>
        <SectionHeader title="Forwards" count={forwards.length} />
        {forwards.length === 0 ? (
          <EmptyState
            title="No forwards yet"
            description="A forward publishes one LAN service — say, Home Assistant at http://192.168.2.50:8123 — at a stable public HTTPS URL, tunneled out over WireGuard with no port forwarding or dynamic DNS."
          >
            <Button size="sm" onPress={openAdd}>
              <ButtonText>Add forward</ButtonText>
            </Button>
          </EmptyState>
        ) : (
          <VStack>
            {forwards.map((item) => (
              <ForwardRow
                key={item.Name}
                item={item}
                toggleBusy={togglePending === item.Name}
                onToggle={toggleForward}
                onDelete={setDeleteTarget}
                onCopy={(text) => copyText(text, alert)}
              />
            ))}
          </VStack>
        )}
      </Card>

      <Card>
        <SectionHeader
          title="Relay"
          right={
            config?.AuthTokenConfigured ? (
              <Badge action="success" variant="outline" borderRadius="$full">
                <BadgeText>Token configured ✓</BadgeText>
              </Badge>
            ) : null
          }
        />
        <VStack space="md">
          <TextField
            label="Relay domain"
            value={apiDomain}
            onChangeText={setApiDomain}
            placeholder="l.tunwg.com"
            helper="Leave empty for the public tunwg relay. Set your own domain if you self-host a tunwg server (TUNWG_API)."
          />
          {config?.AuthTokenConfigured && !replaceToken ? (
            <VStack space="xs">
              <Text
                size="sm"
                fontWeight="$semibold"
                color="$textLight800"
                sx={{ _dark: { color: '$textDark100' } }}
              >
                Relay auth token
              </Text>
              <HStack space="sm" alignItems="center">
                <Badge action="success" variant="outline" borderRadius="$full">
                  <BadgeText>Configured ✓</BadgeText>
                </Badge>
                <Button
                  size="xs"
                  variant="link"
                  onPress={() => setReplaceToken(true)}
                >
                  <ButtonText>Replace</ButtonText>
                </Button>
                <Button
                  size="xs"
                  variant="link"
                  onPress={() => setShowRemoveToken(true)}
                >
                  <ButtonText>Remove</ButtonText>
                </Button>
              </HStack>
              <Text size="xs" color="$muted500">
                TUNWG_AUTH is set. It is stored server-side and never shown
                again.
              </Text>
            </VStack>
          ) : (
            <VStack space="xs">
              <TextField
                label="Relay auth token"
                value={authToken}
                onChangeText={setAuthToken}
                placeholder={
                  config?.AuthTokenConfigured
                    ? 'Enter new token'
                    : 'Not set — only needed for self-hosted relays'
                }
                helper="TUNWG_AUTH for a self-hosted relay. Stored server-side, never shown again."
                secureTextEntry
              />
              {config?.AuthTokenConfigured ? (
                <Button
                  size="xs"
                  variant="link"
                  alignSelf="flex-start"
                  onPress={() => {
                    setReplaceToken(false)
                    setAuthToken('')
                  }}
                >
                  <ButtonText>Keep existing token</ButtonText>
                </Button>
              ) : null}
            </VStack>
          )}
          <HStack
            space="md"
            alignItems="center"
            justifyContent="space-between"
            flexWrap="wrap"
          >
            <Text size="xs" color="$muted500">
              Saving restarts all tunnels.
            </Text>
            <Button
              size="sm"
              isDisabled={!relayDirty || saving}
              onPress={saveRelay}
            >
              <ButtonText>{saving ? 'Saving…' : 'Save relay settings'}</ButtonText>
            </Button>
          </HStack>
        </VStack>
      </Card>

      <ModalForm
        isOpen={showAdd}
        onClose={closeAdd}
        title={addStep === 'form' ? 'Add forward' : 'Publish to the internet?'}
      >
        {addStep === 'form' ? (
          <VStack space="md">
            <TextField
              label="Name"
              value={newForward.Name}
              onChangeText={(v) => setNewForward({ ...newForward, Name: v })}
              placeholder="home-assistant"
              helper="Lowercase letters, digits and dashes"
              error={nameError}
            />
            <TextField
              label="Local URL"
              value={newForward.LocalURL}
              onChangeText={(v) =>
                setNewForward({ ...newForward, LocalURL: v })
              }
              placeholder="http://192.168.2.50:8123"
              helper="Private LAN address required — 192.168.x.x, 10.x.x.x or 172.16-31.x.x. Hostnames and localhost are rejected."
              error={urlError}
            />
            <TextField
              label="Key name (optional)"
              value={newForward.Key}
              onChangeText={(v) => setNewForward({ ...newForward, Key: v })}
              placeholder="defaults to the forward name"
              helper="TUNWG_KEY: names the WireGuard key so the public subdomain stays stable"
              error={keyError}
              secureTextEntry
            />
            <TextField
              label="Basic auth (optional)"
              value={newForward.Auth}
              onChangeText={(v) => setNewForward({ ...newForward, Auth: v })}
              placeholder="user:$2y$05$... (htpasswd -nbB user pass)"
              helper="Strongly recommended: require credentials before the tunnel reaches your service"
              error={authError}
              secureTextEntry
            />
            <HStack
              space="md"
              alignItems="center"
              justifyContent="space-between"
            >
              <VStack flex={1} space="xs">
                <Text
                  size="sm"
                  fontWeight="$semibold"
                  color="$textLight800"
                  sx={{ _dark: { color: '$textDark100' } }}
                >
                  Relay over HTTPS
                </Text>
                <Text size="xs" color="$muted500">
                  Only if outbound UDP is blocked — adds latency
                </Text>
              </VStack>
              <Toggle
                value={!!newForward.Relay}
                onPress={() =>
                  setNewForward({ ...newForward, Relay: !newForward.Relay })
                }
                label="Relay over HTTPS"
              />
            </HStack>
            <HStack space="md" justifyContent="flex-end">
              <Button
                size="sm"
                variant="outline"
                action="secondary"
                onPress={closeAdd}
              >
                <ButtonText>Cancel</ButtonText>
              </Button>
              <Button
                size="sm"
                isDisabled={!formValid}
                onPress={() => setAddStep('confirm')}
              >
                <ButtonText>Continue</ButtonText>
              </Button>
            </HStack>
          </VStack>
        ) : (
          <VStack space="md">
            <Text size="sm">
              {`This publishes ${draftURL} to the public internet. Continue?`}
            </Text>
            <Text size="xs" color="$muted500" lineHeight="$sm">
              {`Anyone with the URL can reach the service through ${relayDomain}, and the new hostname will appear in public certificate transparency logs.` +
                (newForward.Auth.trim()
                  ? ' Visitors must pass the basic auth you configured.'
                  : ' No basic auth is set, so no credentials are required.')}
            </Text>
            <HStack space="md" justifyContent="flex-end">
              <Button
                size="sm"
                variant="outline"
                action="secondary"
                onPress={() => setAddStep('form')}
              >
                <ButtonText>Back</ButtonText>
              </Button>
              <Button size="sm" isDisabled={adding} onPress={submitAdd}>
                <ButtonText>
                  {adding ? 'Publishing…' : 'Publish forward'}
                </ButtonText>
              </Button>
            </HStack>
          </VStack>
        )}
      </ModalForm>

      <ModalConfirm
        isOpen={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={deleteForward}
        title={`Delete forward ${deleteTarget?.Name}?`}
        message="Publishing stops immediately and the public URL stops resolving to your service. The stored WireGuard key is kept, so re-creating a forward with the same key name gets the same URL."
        confirmText="Delete"
        destructive
      />

      <ModalConfirm
        isOpen={showRemoveToken}
        onClose={() => setShowRemoveToken(false)}
        onConfirm={removeToken}
        title="Remove relay auth token?"
        message="The token is deleted and all tunnels restart without TUNWG_AUTH. Forwards using a self-hosted relay that requires the token will fail to reconnect."
        confirmText="Remove token"
        destructive
      />
    </Page>
  )
}
