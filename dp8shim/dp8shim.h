#pragma once

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// NOTE: This DLL is loaded by a 64-bit Go process. Keep exports and structs stable.

// Start a DirectPlay8 server on the given port.
// Returns HRESULT (S_OK == 0) as a 32-bit int.
__declspec(dllexport) int32_t DP8_StartServer(uint16_t port);

// Stop and release the DirectPlay8 server (best-effort).
__declspec(dllexport) void DP8_StopServer(void);

// DP8Event is a small "pull" interface for DP8 callback events.
// Go polls DP8_PopEvent instead of receiving COM callbacks directly.
//
// msg_id: one of DPN_MSGID_* values (ex: 0xFFFF0011 for RECEIVE).
// dpnid:  sender/player id for RECEIVE, or related player id for create/destroy.
// data_len: number of bytes copied into caller buffer (may be truncated).
// flags: bit0 set if data was truncated to fit in caller buffer.
// ts_unix_ms: event timestamp (UTC milliseconds since Unix epoch).
#pragma pack(push, 1)
typedef struct DP8Event
{
    uint32_t msg_id;
    uint32_t dpnid;
    uint32_t data_len;
    uint32_t flags;
    uint64_t ts_unix_ms;
} DP8Event;
#pragma pack(pop)

// Pop the next queued event.
// Returns:
// - 1 if an event was returned
// - 0 if no events are available
// - negative on internal error
//
// Parameters:
// - outEvt: required
// - outBuf/outCap: optional; if outBuf is NULL or outCap==0, payload is dropped
__declspec(dllexport) int32_t DP8_PopEvent(DP8Event* outEvt, uint8_t* outBuf, uint32_t outCap);

// Returns current queued event count (best-effort).
__declspec(dllexport) uint32_t DP8_GetQueueDepth(void);

// Send bytes to a connected player.
// Returns HRESULT as int32.
__declspec(dllexport) int32_t DP8_SendTo(uint32_t dpnid, const uint8_t* buf, uint32_t len, uint32_t flags);

#ifdef __cplusplus
}
#endif
