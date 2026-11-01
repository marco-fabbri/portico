# ADR 0010: The stored peering state follows Prism Central, and a slow Prism Central never turns a read into a destructive action

- Status: accepted (2026-11-01)
- Deciders: Marco (the acceptance's findings), Claude

## Context

The prefix routing design (2026-11-01) stores the site's peering in the
settings: its kind (static, router, edge), its objects' extIds and the
state of the last operation. The acceptance on the lab (write-up 39) ran
while Prism Central answered `504 upstream request timeout` in waves of
two to four minutes, and three things went wrong in a row: a move to the
router stored "router" before its first step, failed on a read, and
"Delete the edge router" then trusted the store and deleted the live
peer; the delete stored the "building" mark the card had set as its
final state and the card waited for ever; a remote gateway update that
got a transient answer would have recreated the remote and deleted the
session. The final review of the branch listed the same theme in other
places: the monitor writing a whole peering it had read before its
calls, the waits giving up on one transient answer, the Save of the
Networking card tearing the peering down because a check failed.

## Decision

1. **The stored kind follows the remote gateway in Prism Central**, not
   the operation asked for: a move changes the kind inside the step that
   updates the remote gateway, after it succeeded. An edge built before
   a failed move is a kept one (the card can delete it).
2. **Every operation ends with the session's state read back**, never
   with the mark its caller set; a peering stored as building when the
   process starts belongs to an operation nobody will finish and is
   failed with "Portico restarted while the operation ran; press Repair".
3. **A transient answer is never an outcome.** The waits for the gateway
   and the session poll through 500/502/503/504 within their bound; a
   remote gateway update is recreated only on a refusal (a 4xx or a
   failed task), anything else fails the step for Repair to run again;
   the gateway's 504 repeats task polls only, never a create (its task
   may still be running; Repair adopts by name). The monitor stores only
   the state, the message and the time, and only over the objects it
   read, with no operation running.
4. **Only the MSP's hand tears the peering down**: unticking the switch,
   or choosing the static peer. A switch Portico forced off because a
   check failed keeps the peering.
5. **The VLAN router of the Routing section is the router that forwards
   the routed prefix**: the default route of the gateway and of the edge
   router, and the external subnet's gateway when the router peers. A
   router-mode build sets that gateway when the session is up.
6. **The core's static route to a kept edge is to be removed as soon as
   the router peers**, not once the edge is deleted: on a router that
   runs FRR a static route keeps the BGP route out of the kernel whatever
   its metric. The card reminds it until the edge is deleted, as the
   design says, with that wording.

## Consequences

- A failed move leaves the card with the old peer and its actions; the
  MSP presses Repair or moves again. No action can delete the live peer.
- After a transient failure the state stays `failed` until Repair: the
  monitor leaves a failure alone, because the session may be `UP` with
  the old peer. The release notes say so.
- An MSP who edits the DNS while the session flaps keeps the gateway,
  the session and the edge.
- The design document's sections 4.5 and 4.6 read with these rules; the
  measurements are in write-up 39.
