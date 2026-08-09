# Lewp

Lewp gives local project instances stable browser identities without managing their application processes.

## Language

**Route**:
A hostname and loopback-port allocation owned by a project-instance directory. A route may also own aliases and wildcard hosts.

**Bare port**:
A named loopback-port allocation owned by a directory but not associated with a hostname or proxy route.
_Avoid_: Route

**Release**:
Ending an active allocation while retaining its remembered identity and history.
_Avoid_: Delete, remove

**Forget**:
Permanently removing an allocation's remembered identity and history, whether it is active or already released.
_Avoid_: Release
