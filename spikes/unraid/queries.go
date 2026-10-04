package main

// Read queries the v0.1 integration would run, written against the Unraid API schema
// (api/generated-schema.graphql in github.com/unraid/api, v4.37.x). Each runs on its own so
// a field that doesn't exist on an older API only fails that one query.
//
// Deliberately left out:
//   - Docker sizeRootFs/sizeRw/sizeLog: they make the server run `docker ps --size` or stat logs.
//   - Feature-flagged container fields (isUpdateAvailable, isRebuildReady, …): hidden unless the
//     server enables ENABLE_NEXT_DOCKER_RELEASE.
//   - Nothing else about disks: `disks` is the expensive read. Whatever fields are asked, the
//     resolver runs systeminformation's diskLayout(), which runs `smartctl -a -j` on every disk
//     WITHOUT `-n standby` (systeminformation 5.31.7, lib/filesystem.js), so it can wake
//     spun-down disks. `temperature` adds one `smartctl -n standby -A -j` per disk. Both are
//     asked once, in Disks; SpinBeforeDisks/SpinAfterDisks read emhttp's spin state (free)
//     around it to see whether it woke anything. array.disks.temp is the cheap temperature.
//   - Serial numbers on info.os and info.system, and anything about the license.

// readQuery is one named read. Scope is the Unraid resource the key needs for it.
type readQuery struct {
	Name  string
	Scope string
	Doc   string
}

var readQueries = []readQuery{
	{"Info", "info:read_any", `query Info {
  info {
    time
    machineId
    os { platform distro release kernel arch hostname uptime uefi }
    cpu { manufacturer brand cores threads }
    system { manufacturer model uuid virtual }
    versions { core { unraid api kernel } }
  }
}`},
	{"Network", "info:read_any", `query Network {
  info {
    networkInterfaces { id name macAddress ipAddress netmask gateway useDhcp virtual operstate type }
  }
}`},
	{"Metrics", "info:read_any", `query Metrics {
  metrics {
    cpu { percentTotal }
    memory { total used free available active buffcache percentTotal swapTotal swapUsed percentSwapTotal }
  }
}`},
	{"Array", "array:read_any", `query Array {
  array {
    id
    state
    capacity { kilobytes { free used total } disks { free used total } }
    parityCheckStatus { status progress errors running paused correcting date duration speed }
    boot { ...D }
    parities { ...D }
    disks { ...D }
    caches { ...D }
  }
}
fragment D on ArrayDisk {
  id idx name device size status rotational temp numReads numWrites numErrors
  fsSize fsFree fsUsed exportable type warning critical fsType comment format transport color isSpinning
}`},
	{"ParityHistory", "array:read_any", `query ParityHistory {
  parityHistory { date duration speed status errors }
}`},
	{"SpinBeforeDisks", "array:read_any", spinDoc("SpinBeforeDisks")},
	{"Disks", "disk:read_any", `query Disks {
  disks {
    id device type name vendor size firmwareRevision serialNum interfaceType smartStatus isSpinning temperature
    partitions { name fsType size }
  }
}`},
	{"SpinAfterDisks", "array:read_any", spinDoc("SpinAfterDisks")},
	{"Docker", "docker:read_any", `query Docker {
  docker {
    id
    containers {
      id names image imageId state status created autoStart autoStartOrder autoStartWait
      ports { ip privatePort publicPort type }
      lanIpPorts
      labels
      networkSettings
      mounts
      hostConfig { networkMode }
      templatePath
      webUiUrl
      iconUrl
      isOrphaned
    }
  }
}`},
	{"DockerNetworks", "docker:read_any", `query DockerNetworks {
  docker { networks { id name driver scope } }
}`},
	{"Vms", "vms:read_any", `query Vms {
  vms { id domains { id name state } }
}`},
	{"Shares", "share:read_any", `query Shares {
  shares { id name comment free used size include exclude cache allocator splitLevel floor cow color luksStatus }
}`},
	{"Notifications", "notifications:read_any", `query Notifications {
  notifications {
    overview {
      unread { info warning alert total }
      archive { info warning alert total }
    }
    list(filter: { type: UNREAD, offset: 0, limit: 20 }) {
      id title subject description importance link type timestamp
    }
    warningsAndAlerts { id title subject importance timestamp }
  }
}`},
	{"Me", "me:read_any", `query Me {
  me { id name description roles permissions { resource actions } }
}`},
}

// spinDoc reads emhttp's spin state and temperature for every array and pool device: no ids,
// so no serials, and no smartctl.
func spinDoc(name string) string {
	return `query ` + name + ` {
  array {
    boot { name isSpinning temp }
    parities { name isSpinning temp }
    disks { name isSpinning temp }
    caches { name isSpinning temp }
  }
}`
}

// outOfScope are reads the consented key must NOT be able to do. They ask for ids only, so
// even an unexpected success returns nothing sensitive. Expect FORBIDDEN (or similar).
var outOfScope = []readQuery{
	{"OutOfScopeUps", "config:read_any (UPS lives under CONFIG)", `query OutOfScopeUps { upsDevices { id } }`},
	{"OutOfScopeApiKeys", "api_key:read_any (not even role:viewer has it)", `query OutOfScopeApiKeys { apiKeys { id } }`},
	{"OutOfScopeServers", "servers:read_any", `query OutOfScopeServers { server { id } }`},
	{"OutOfScopeLogs", "logs:read_any", `query OutOfScopeLogs { logFiles { name } }`},
	{"OutOfScopeFlash", "flash:read_any", `query OutOfScopeFlash { flash { id } }`},
	{"OutOfScopeVars", "vars:read_any", `query OutOfScopeVars { vars { id } }`},
	// The extra resources the first key carried. OS and WELCOME gate no resolver on 4.37.2, so
	// there's nothing to probe for them.
	{"OutOfScopeCloud", "cloud:read_any", `query OutOfScopeCloud { cloud { error } }`},
	{"OutOfScopeNetwork", "network:read_any", `query OutOfScopeNetwork { network { id } }`},
	{"OutOfScopeOnline", "online:read_any", `query OutOfScopeOnline { online }`},
	{"OutOfScopeServices", "services:read_any", `query OutOfScopeServices { services { id } }`},
}

// probeOnlyIfRefused: out-of-scope probes whose resolver has side effects if it runs. They're
// skipped unless me shows the key lacks the scope. `cloud` makes the server check its
// connection to Unraid's cloud (DNS and HTTP from the server).
var probeOnlyIfRefused = map[string]string{"OutOfScopeCloud": "cloud:read_any"}

// subscriptions opened together on one socket for the 60 s window.
var subscriptions = []Sub{
	{"SubCpu", `subscription SubCpu { systemMetricsCpu { percentTotal } }`},
	{"SubMemory", `subscription SubMemory { systemMetricsMemory { percentTotal used total } }`},
	{"SubArray", `subscription SubArray { arraySubscription { state capacity { kilobytes { free used total } } parityCheckStatus { status progress } } }`},
	{"SubParity", `subscription SubParity { parityHistorySubscription { status progress date } }`},
	{"SubDockerStats", `subscription SubDockerStats { dockerContainerStats { id cpuPercent memPercent memUsage netIO blockIO } }`},
	{"SubNotificationsOverview", `subscription SubNotificationsOverview { notificationsOverview { unread { info warning alert total } } }`},
	{"SubNotificationAdded", `subscription SubNotificationAdded { notificationAdded { id title importance timestamp } }`},
	// Out of scope on purpose (CONFIG): proves the scope check also applies to subscriptions.
	{"SubUpsOutOfScope", `subscription SubUpsOutOfScope { upsUpdates { id status } }`},
}

// rootTypeQueries list the root fields by name. The server blocks full introspection
// (anything with "{ __schema {" or operationName IntrospectionQuery) unless the GraphQL
// sandbox is enabled, but deliberately allows __type, which ordinary clients use.
// We use it only for the three root types: enough to show that mutations exist.
var rootTypeQueries = []readQuery{
	{"RootQueryFields", "none (type metadata)", `query RootQueryFields { __type(name: "Query") { name fields { name } } }`},
	{"RootMutationFields", "none (type metadata)", `query RootMutationFields { __type(name: "Mutation") { name fields { name } } }`},
	{"RootSubscriptionFields", "none (type metadata)", `query RootSubscriptionFields { __type(name: "Subscription") { name fields { name } } }`},
}

// introspectionQuery is graphql-js's getIntrospectionQuery() (descriptions on, no
// specifiedByURL/oneOf extras), so the saved result converts to SDL with standard tools.
const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
    directives {
      name
      description
      locations
      args { ...InputValue }
    }
  }
}
fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated
    deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes { ...TypeRef }
}
fragment InputValue on __InputValue {
  name
  description
  type { ...TypeRef }
  defaultValue
}
fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType { kind name }
            }
          }
        }
      }
    }
  }
}`
