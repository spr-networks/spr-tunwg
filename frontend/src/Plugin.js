import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  api,
  useAlert,
  Page,
  ListHeader,
  Card,
  SectionHeader,
  StatTile,
  KeyVal,
  StatusDot,
  Toggle,
  TextField,
  ModalForm,
  ModalConfirm,
  Loading,
  EmptyState,
  Button,
  ButtonText,
  HStack,
  VStack,
  Text,
  Box
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

const WarningBanner = () => (
  <Card>
    <HStack space="sm" alignItems="center">
      <StatusDot warn />
      <VStack flex={1}>
        <Text size="sm" bold>
          Every forward publishes an internal service to the public internet
        </Text>
        <Text size="xs" color="$muted500">
          Tunnels are outward-facing: anyone who discovers the URL can reach
          the target device through the tunwg.dev/tunwg.com relay, and new
          hostnames appear in public certificate transparency logs. Only
          expose services you intend to make reachable from anywhere, and
          prefer adding basic auth.
        </Text>
      </VStack>
    </HStack>
  </Card>
)

const ForwardRow = ({ item, onToggle, onDelete, alert }) => {
  const st = item.Status || {}
  const online = !!st.Running && !!st.PublicURL
  const warn = item.Enabled && !online
  const publicURL = st.PublicURL || ''

  return (
    <Box
      py="$3"
      borderBottomWidth={1}
      borderColor="$borderColorCardLight"
      sx={{ _dark: { borderColor: '$borderColorCardDark' } }}
    >
      <HStack space="md" alignItems="center" flexWrap="wrap">
        <StatusDot online={online} warn={warn} />
        <VStack flex={1} minWidth={220}>
          <HStack space="sm" alignItems="center">
            <Text bold>{item.Name}</Text>
            {item.AuthConfigured ? (
              <Text size="xs" color="$muted500">
                basic auth
              </Text>
            ) : null}
            {item.Relay ? (
              <Text size="xs" color="$muted500">
                https relay
              </Text>
            ) : null}
          </HStack>
          <Text size="xs" fontFamily="$mono" color="$muted500">
            {item.LocalURL}
          </Text>
          {publicURL ? (
            <Text size="xs" fontFamily="$mono">
              {publicURL}
            </Text>
          ) : (
            <Text size="xs" color="$muted500">
              {item.Enabled
                ? st.LastError
                  ? `waiting for tunnel: ${st.LastError}`
                  : 'waiting for public URL...'
                : 'disabled'}
            </Text>
          )}
        </VStack>
        <HStack space="sm" alignItems="center">
          <Button
            size="xs"
            variant="outline"
            isDisabled={!publicURL}
            onPress={() => copyText(publicURL, alert)}
          >
            <ButtonText>Copy URL</ButtonText>
          </Button>
          <Toggle value={!!item.Enabled} onPress={() => onToggle(item)} />
          <Button
            size="xs"
            variant="outline"
            action="negative"
            onPress={() => onDelete(item)}
          >
            <ButtonText>Delete</ButtonText>
          </Button>
        </HStack>
      </HStack>
    </Box>
  )
}

export default function Plugin() {
  const alert = useAlert()
  const [loading, setLoading] = useState(true)
  const [status, setStatus] = useState(null)
  const [forwards, setForwards] = useState([])
  const [config, setConfig] = useState(null)

  const [showAdd, setShowAdd] = useState(false)
  const [newForward, setNewForward] = useState(emptyForward)
  const [deleteTarget, setDeleteTarget] = useState(null)

  const [apiDomain, setApiDomain] = useState('')
  const [authToken, setAuthToken] = useState('')

  const timerRef = useRef(null)

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
        if (cfg) {
          setConfig(cfg)
          setApiDomain(cfg.APIDomain || '')
        }
      })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    refresh(true).catch((err) => alert.error('Failed to load plugin data', err))
    timerRef.current = setInterval(() => {
      refresh(false).catch(() => {})
    }, 5000)
    return () => clearInterval(timerRef.current)
  }, [refresh])

  const addForward = () => {
    const body = {
      Name: newForward.Name.trim(),
      LocalURL: newForward.LocalURL.trim(),
      Key: newForward.Key.trim(),
      Auth: newForward.Auth.trim(),
      Relay: !!newForward.Relay,
      Enabled: !!newForward.Enabled
    }
    api
      .post(`${PLUGIN_BASE}/forwards`, body)
      .then(() => {
        setShowAdd(false)
        setNewForward(emptyForward)
        alert.success(`Forward ${body.Name} added`)
        return refresh(false)
      })
      .catch((err) => alert.error('Failed to add forward', err))
  }

  const toggleForward = (item) => {
    api
      .post(`${PLUGIN_BASE}/forwards/${item.Name}/toggle`, {})
      .then(() => refresh(false))
      .catch((err) => alert.error('Failed to toggle forward', err))
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

  const saveConfig = () => {
    api
      .put(`${PLUGIN_BASE}/config`, {
        APIDomain: apiDomain.trim(),
        AuthToken: authToken.trim()
      })
      .then((cfg) => {
        setConfig(cfg)
        setAuthToken('')
        alert.success('Relay settings saved, tunnels restarting')
        return refresh(false)
      })
      .catch((err) => alert.error('Failed to save settings', err))
  }

  if (loading) {
    return (
      <Page>
        <Loading text="Loading tunwg forwards..." />
      </Page>
    )
  }

  const running = status?.ForwardsRunning || 0
  const total = status?.ForwardsTotal || 0

  return (
    <Page>
      <ListHeader
        title="Tunwg"
        description="Expose local services with public HTTPS URLs over outbound WireGuard tunnels"
      >
        <Button size="sm" onPress={() => setShowAdd(true)}>
          <ButtonText>Add Forward</ButtonText>
        </Button>
      </ListHeader>

      <WarningBanner />

      <Card>
        <SectionHeader
          title="Status"
          right={<StatusDot online={total === 0 || running > 0} warn={total > 0 && running < total} />}
        />
        <HStack flexWrap="wrap" gap="$2">
          <StatTile label="Forwards" value={`${running}/${total} up`} />
          <StatTile label="Relay" value={status?.APIDomain || 'l.tunwg.com'} mono />
          <StatTile label="tunwg" value={status?.TunwgVersion || '—'} mono />
        </HStack>
      </Card>

      <Card>
        <SectionHeader title="Forwards" count={forwards.length} />
        {forwards.length === 0 ? (
          <EmptyState
            title="No forwards yet"
            description="Add a forward to publish a LAN service (e.g. http://192.168.2.50:8123) at a public HTTPS URL."
          >
            <Button size="sm" onPress={() => setShowAdd(true)}>
              <ButtonText>Add Forward</ButtonText>
            </Button>
          </EmptyState>
        ) : (
          <VStack>
            {forwards.map((item) => (
              <ForwardRow
                key={item.Name}
                item={item}
                alert={alert}
                onToggle={toggleForward}
                onDelete={setDeleteTarget}
              />
            ))}
          </VStack>
        )}
      </Card>

      <Card>
        <SectionHeader title="Relay Server" />
        <VStack space="md">
          <TextField
            label="API Domain"
            value={apiDomain}
            onChangeText={setApiDomain}
            placeholder="l.tunwg.com"
            helper="Leave empty for the public tunwg relay. Set to your own domain if you self-host a tunwg server (TUNWG_API)."
          />
          <TextField
            label="Relay Auth Token"
            value={authToken}
            onChangeText={setAuthToken}
            placeholder={
              config?.AuthTokenConfigured ? 'configured (unchanged)' : 'optional'
            }
            helper="TUNWG_AUTH for self-hosted relays. Stored server-side, never displayed again."
            secureTextEntry
          />
          <HStack>
            <Button size="sm" onPress={saveConfig}>
              <ButtonText>Save Relay Settings</ButtonText>
            </Button>
          </HStack>
        </VStack>
      </Card>

      <ModalForm
        isOpen={showAdd}
        onClose={() => setShowAdd(false)}
        title="Add Forward"
      >
        <VStack space="md">
          <TextField
            label="Name"
            value={newForward.Name}
            onChangeText={(v) => setNewForward({ ...newForward, Name: v })}
            placeholder="home-assistant"
            helper="Lowercase letters, digits and dashes"
          />
          <TextField
            label="Local URL"
            value={newForward.LocalURL}
            onChangeText={(v) => setNewForward({ ...newForward, LocalURL: v })}
            placeholder="http://192.168.2.50:8123"
            helper="http(s) URL of a LAN device (private address required; localhost is rejected)"
          />
          <TextField
            label="Key Name (optional)"
            value={newForward.Key}
            onChangeText={(v) => setNewForward({ ...newForward, Key: v })}
            placeholder="defaults to the forward name"
            helper="TUNWG_KEY: names the WireGuard key so the public subdomain stays stable"
            secureTextEntry
          />
          <TextField
            label="Basic Auth (optional)"
            value={newForward.Auth}
            onChangeText={(v) => setNewForward({ ...newForward, Auth: v })}
            placeholder="user:$2y$05$... (htpasswd -nbB user pass)"
            helper="Strongly recommended: require credentials before the tunnel reaches your service"
            secureTextEntry
          />
          <KeyVal
            label="Relay over HTTPS"
            value={newForward.Relay ? 'on (use only if UDP is blocked)' : 'off'}
          />
          <HStack justifyContent="space-between" alignItems="center">
            <Toggle
              value={!!newForward.Relay}
              onPress={() =>
                setNewForward({ ...newForward, Relay: !newForward.Relay })
              }
            />
            <Button size="sm" onPress={addForward}>
              <ButtonText>Add Forward</ButtonText>
            </Button>
          </HStack>
          <Text size="xs" color="$muted500">
            The service becomes reachable from the public internet as soon as
            the tunnel connects.
          </Text>
        </VStack>
      </ModalForm>

      <ModalConfirm
        isOpen={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={deleteForward}
        title={`Delete forward ${deleteTarget?.Name}?`}
        message="The tunnel is stopped and its public URL stops resolving to your service. The stored WireGuard key is kept so re-creating the forward with the same key name restores the same URL."
        confirmText="Delete"
        destructive
      />
    </Page>
  )
}
