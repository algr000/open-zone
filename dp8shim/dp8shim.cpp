// Minimal DirectPlay8 server shim.
//
// Purpose: let dpnet.dll implement the DP8 wire handshake so our Go code can
// focus on higher-level behavior.

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#define INITGUID
#include <initguid.h>

#include <stdio.h>
#include <stdarg.h>
#include <string.h>
#include <time.h>

// Used to locate our module path for log file placement.
extern "C" IMAGE_DOS_HEADER __ImageBase;

// DirectPlay8 headers copied into this repo.
#include "include/dplay8.h"
#include "include/dpaddr.h"

#include "dp8shim.h"

static IDirectPlay8Server* g_dpServer = NULL;
static IDirectPlay8Address* g_deviceAddr = NULL;
static bool g_comInit = false;
static FILE* g_log = NULL;
static CRITICAL_SECTION g_logCs;
static bool g_logCsInit = false;
static DPNID g_lastClient = 0;
static unsigned long g_indicateCount = 0;

// File logging is intentionally OFF by default. The shim is a transport bridge; the Go
// runtime is the primary place to capture operational logs.
static const bool kEnableFileLog = false;

// SendTo context for async sends. We copy outgoing payloads into heap memory so the caller
// (Go) does not need to keep buffers alive until DPN_MSGID_SEND_COMPLETE.
typedef struct SendCtx
{
    BYTE* buf;
    DWORD len;
} SendCtx;

// From DungeonSiegeSrc/lib projects/netpipe/NetPipe.h: enum eNetPipeEvent (starts at 0).
// NE_SESSION_CONNECTED is the 17th value (0-based index 16).
static const DWORD kNetPipe_NE_SESSION_CONNECTED = 16;

// We are migrating app-protocol logic into Go. The shim should only capture + forward.
static const bool kAutoReplyOnConnectXml = false;

// Simple event queue for Go polling.
// Keep payload size generous but bounded; these messages are currently tiny XML-ish frames.
static const uint32_t kQCap = 512;
static const uint32_t kQMaxPayload = 16 * 1024;
static CRITICAL_SECTION g_qCs;
static bool g_qCsInit = false;

typedef struct QItem
{
    DP8Event evt;
    uint32_t used;
    uint8_t data[kQMaxPayload];
} QItem;

static QItem g_q[kQCap];
static uint32_t g_qHead = 0;
static uint32_t g_qTail = 0;
static uint32_t g_qLen = 0;

static uint64_t unix_ms_now()
{
    FILETIME ft;
    GetSystemTimeAsFileTime(&ft);
    ULARGE_INTEGER ui;
    ui.LowPart = ft.dwLowDateTime;
    ui.HighPart = ft.dwHighDateTime;
    // Windows epoch (1601) -> Unix epoch (1970): 11644473600 seconds.
    uint64_t t100ns = ui.QuadPart;
    uint64_t ms = (t100ns / 10000ULL);
    return ms - (11644473600ULL * 1000ULL);
}

static void q_init_if_needed()
{
    if (g_qCsInit)
        return;
    InitializeCriticalSection(&g_qCs);
    g_qCsInit = true;
}

static void q_push(uint32_t msgId, uint32_t dpnid, const uint8_t* data, uint32_t n)
{
    q_init_if_needed();

    EnterCriticalSection(&g_qCs);
    if (g_qLen == kQCap)
    {
        // Drop oldest.
        g_qHead = (g_qHead + 1) % kQCap;
        g_qLen--;
    }

    QItem* it = &g_q[g_qTail];
    ZeroMemory(it, sizeof(*it));
    it->evt.msg_id = msgId;
    it->evt.dpnid = dpnid;
    it->evt.ts_unix_ms = unix_ms_now();
    it->evt.flags = 0;

    if (data && n > 0)
    {
        uint32_t take = n;
        if (take > kQMaxPayload)
        {
            take = kQMaxPayload;
            it->evt.flags |= 1; // truncated
        }
        memcpy(it->data, data, take);
        it->used = take;
        it->evt.data_len = take;
    }
    else
    {
        it->used = 0;
        it->evt.data_len = 0;
    }

    g_qTail = (g_qTail + 1) % kQCap;
    g_qLen++;
    LeaveCriticalSection(&g_qCs);
}

static void open_log_if_needed()
{
    if (!kEnableFileLog)
        return;
    if (g_log)
        return;
    if (!g_logCsInit)
    {
        InitializeCriticalSection(&g_logCs);
        g_logCsInit = true;
    }

    // Write next to the DLL.
    char path[MAX_PATH];
    DWORD n = GetModuleFileNameA((HMODULE)&__ImageBase, path, MAX_PATH);
    if (n == 0 || n >= MAX_PATH)
    {
        g_log = fopen("dp8shim.log", "a");
        return;
    }
    // Replace filename with dp8shim.log
    for (int i = (int)n - 1; i >= 0; i--)
    {
        if (path[i] == '\\' || path[i] == '/')
        {
            path[i + 1] = '\0';
            break;
        }
    }
    strcat_s(path, sizeof(path), "dp8shim.log");
    g_log = fopen(path, "a");
}

static void logline(const char* fmt, ...)
{
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    OutputDebugStringA(buf);

    if (!kEnableFileLog)
        return;

    open_log_if_needed();
    if (g_logCsInit)
        EnterCriticalSection(&g_logCs);
    if (g_log)
    {
        // Simple timestamp prefix.
        SYSTEMTIME st;
        GetLocalTime(&st);
        fprintf(g_log, "%04u-%02u-%02u %02u:%02u:%02u.%03u %s",
                st.wYear, st.wMonth, st.wDay,
                st.wHour, st.wMinute, st.wSecond, st.wMilliseconds,
                buf);
        fflush(g_log);
    }
    if (g_logCsInit)
        LeaveCriticalSection(&g_logCs);
}

static const char* dp8_msg_name(DWORD id)
{
    switch (id)
    {
    case DPN_MSGID_ENUM_HOSTS_QUERY: return "ENUM_HOSTS_QUERY";
    case DPN_MSGID_ENUM_HOSTS_RESPONSE: return "ENUM_HOSTS_RESPONSE";
    case DPN_MSGID_INDICATE_CONNECT: return "INDICATE_CONNECT";
    case DPN_MSGID_CONNECT_COMPLETE: return "CONNECT_COMPLETE";
    case DPN_MSGID_CREATE_PLAYER: return "CREATE_PLAYER";
    case DPN_MSGID_DESTROY_PLAYER: return "DESTROY_PLAYER";
    case DPN_MSGID_TERMINATE_SESSION: return "TERMINATE_SESSION";
    case DPN_MSGID_RECEIVE: return "RECEIVE";
    case DPN_MSGID_SEND_COMPLETE: return "SEND_COMPLETE";
    case DPN_MSGID_RETURN_BUFFER: return "RETURN_BUFFER";
    default: return NULL;
    }
}

static const char* dp8_hr_name(HRESULT hr)
{
    switch (hr)
    {
    case S_OK: return "S_OK";
    case DPNERR_INVALIDFLAGS: return "DPNERR_INVALIDFLAGS";
    case DPNERR_INVALIDPLAYER: return "DPNERR_INVALIDPLAYER";
    case DPNERR_NOCONNECTION: return "DPNERR_NOCONNECTION";
    case DPNERR_NOTREADY: return "DPNERR_NOTREADY";
    case DPNERR_PENDING: return "DPNERR_PENDING";
    default: return NULL;
    }
}

static bool looks_like_text(const BYTE* b, DWORD n)
{
    // Heuristic: allow common whitespace + printable ASCII, tolerate a few NULs.
    DWORD bad = 0;
    for (DWORD i = 0; i < n; i++)
    {
        BYTE c = b[i];
        if (c == 0)
            continue;
        if (c == '\r' || c == '\n' || c == '\t')
            continue;
        if (c >= 0x20 && c <= 0x7E)
            continue;
        bad++;
        if (bad > 2)
            return false;
    }
    return true;
}

static bool extract_attr(const char* s, const char* key, char* out, size_t outCap)
{
    // Very small XML-ish attribute extractor: key="VALUE"
    // Returns false if not found.
    if (!s || !key || !out || outCap == 0)
        return false;
    out[0] = '\0';

    char needle[64];
    snprintf(needle, sizeof(needle), "%s=\"", key);
    const char* p = strstr(s, needle);
    if (!p)
        return false;
    p += strlen(needle);
    const char* q = strchr(p, '"');
    if (!q || q <= p)
        return false;

    size_t n = (size_t)(q - p);
    if (n >= outCap)
        n = outCap - 1;
    memcpy(out, p, n);
    out[n] = '\0';
    return true;
}

static GUID kAppGuid =
    // 77E2D9C2-504E-459F-8416-0848130BBE1E
    { 0x77E2D9C2, 0x504E, 0x459F, { 0x84, 0x16, 0x08, 0x48, 0x13, 0x0B, 0xBE, 0x1E } };

static unsigned long long seconds_since_2000_utc()
{
    // Match UIZoneMatch::GetLocalTimeInSeconds() logic (Jan 1 2000 base, seconds).
    SYSTEMTIME baseSysTime;
    ZeroMemory(&baseSysTime, sizeof(baseSysTime));
    baseSysTime.wYear = 2000;
    baseSysTime.wMonth = 1;
    baseSysTime.wDay = 1;
    FILETIME baseFileTime;
    SystemTimeToFileTime(&baseSysTime, &baseFileTime);
    ULARGE_INTEGER bi;
    bi.LowPart = baseFileTime.dwLowDateTime;
    bi.HighPart = baseFileTime.dwHighDateTime;
    unsigned long long baseSec = bi.QuadPart / 10000000ULL;

    FILETIME nowFt;
    GetSystemTimeAsFileTime(&nowFt);
    ULARGE_INTEGER ni;
    ni.LowPart = nowFt.dwLowDateTime;
    ni.HighPart = nowFt.dwHighDateTime;
    unsigned long long nowSec = ni.QuadPart / 10000000ULL;

    if (nowSec < baseSec)
        return 0;
    return nowSec - baseSec;
}

static void SafeRelease(IUnknown** pp);

static HRESULT WINAPI DP8ServerMsgHandler(PVOID, DWORD dwMessageId, PVOID pMsgBuffer)
{
    // Log DP8 message ids to the debugger (file logging is off by default).
    const char* name = dp8_msg_name(dwMessageId);
    if (name)
        logline("DP8 msg id=0x%08lx (%s)\n", (unsigned long)dwMessageId, name);
    else
        logline("DP8 msg id=0x%08lx\n", (unsigned long)dwMessageId);

    switch (dwMessageId)
    {
    case DPN_MSGID_ENUM_HOSTS_QUERY:
        // Allow enumeration.
        return S_OK;
    case DPN_MSGID_INDICATE_CONNECT:
    {
        PDPNMSG_INDICATE_CONNECT p = (PDPNMSG_INDICATE_CONNECT)pMsgBuffer;
        // Best-effort: capture remote address URL for diagnostics.
        // This may include a hostname (often a machine name). The Go runtime should
        // sanitize before logging.
        char urlA[1024];
        urlA[0] = '\0';
        if (p && p->pAddressPlayer)
        {
            WCHAR url[1024];
            DWORD sz = (DWORD)(sizeof(url) / sizeof(url[0]));
            if (SUCCEEDED(p->pAddressPlayer->GetURLW(url, &sz)))
            {
                // Best-effort ASCII log (URL is mostly ASCII anyway).
                WideCharToMultiByte(CP_UTF8, 0, url, -1, urlA, sizeof(urlA), NULL, NULL);
                logline("INDICATE_CONNECT from=%s userData=%lu\n", urlA, (unsigned long)p->dwUserConnectDataSize);
            }
        }
        if (urlA[0] != '\0')
        {
            q_push(DPN_MSGID_INDICATE_CONNECT, 0, (const uint8_t*)urlA, (uint32_t)strlen(urlA));
        }
        else
        {
            q_push(DPN_MSGID_INDICATE_CONNECT, 0, NULL, 0);
        }
        // Accept the connection request. For now we intentionally return no app-layer reply data.
        // We *did* experiment with NetPipe-style DWORD reply data, but it can confuse the client.
        if (p)
        {
            p->pvReplyData = NULL;
            p->dwReplyDataSize = 0;
            p->pvReplyContext = NULL;
        }
        logline("INDICATE_CONNECT reply=NONE\n");
        return S_OK;
    }
    case DPN_MSGID_CONNECT_COMPLETE:
    {
        PDPNMSG_CONNECT_COMPLETE p = (PDPNMSG_CONNECT_COMPLETE)pMsgBuffer;
        if (p)
        {
            logline("CONNECT_COMPLETE hr=0x%08lx appReplySize=%lu\n",
                    (unsigned long)p->hResultCode, (unsigned long)p->dwApplicationReplyDataSize);
        }
        q_push(DPN_MSGID_CONNECT_COMPLETE, 0, NULL, 0);
        return S_OK;
    }
    case DPN_MSGID_CREATE_PLAYER:
    {
        PDPNMSG_CREATE_PLAYER p = (PDPNMSG_CREATE_PLAYER)pMsgBuffer;
        if (p)
        {
            logline("CREATE_PLAYER dpnid=0x%08lx\n", (unsigned long)p->dpnidPlayer);
            // Best-effort: remember the last created player id.
            g_lastClient = p->dpnidPlayer;
            // Best-effort: capture remote address URL for diagnostics.
            // This may include a hostname (often a machine name). The Go runtime should
            // sanitize before logging.
            char urlA[1024];
            urlA[0] = '\0';
            if (g_dpServer)
            {
                IDirectPlay8Address* pAddr = NULL;
                if (SUCCEEDED(g_dpServer->GetClientAddress(p->dpnidPlayer, &pAddr, 0)) && pAddr)
                {
                    WCHAR urlW[1024];
                    DWORD sz = (DWORD)(sizeof(urlW) / sizeof(urlW[0]));
                    if (SUCCEEDED(pAddr->GetURLW(urlW, &sz)))
                    {
                        WideCharToMultiByte(CP_UTF8, 0, urlW, -1, urlA, sizeof(urlA), NULL, NULL);
                    }
                    SafeRelease((IUnknown**)&pAddr);
                }
            }
            if (urlA[0] != '\0')
            {
                q_push(DPN_MSGID_CREATE_PLAYER, (uint32_t)p->dpnidPlayer, (const uint8_t*)urlA, (uint32_t)strlen(urlA));
            }
            else
            {
                q_push(DPN_MSGID_CREATE_PLAYER, (uint32_t)p->dpnidPlayer, NULL, 0);
            }
        }
        return S_OK;
    }
    case DPN_MSGID_TERMINATE_SESSION:
        q_push(DPN_MSGID_TERMINATE_SESSION, 0, NULL, 0);
        return S_OK;
    case DPN_MSGID_DESTROY_PLAYER:
    {
        PDPNMSG_DESTROY_PLAYER p = (PDPNMSG_DESTROY_PLAYER)pMsgBuffer;
        if (p)
        {
            q_push(DPN_MSGID_DESTROY_PLAYER, (uint32_t)p->dpnidPlayer, NULL, 0);
        }
        return S_OK;
    }
    case DPN_MSGID_RETURN_BUFFER:
    {
        PDPNMSG_RETURN_BUFFER p = (PDPNMSG_RETURN_BUFFER)pMsgBuffer;
        if (p && p->pvUserContext)
        {
            HeapFree(GetProcessHeap(), 0, p->pvUserContext);
        }
        q_push(DPN_MSGID_RETURN_BUFFER, 0, NULL, 0);
        return S_OK;
    }
    case DPN_MSGID_SEND_COMPLETE:
    {
        PDPNMSG_SEND_COMPLETE p = (PDPNMSG_SEND_COMPLETE)pMsgBuffer;
        if (p && p->pvUserContext)
        {
            // Free the copied payload from DP8_SendTo.
            SendCtx* ctx = (SendCtx*)p->pvUserContext;
            if (ctx->buf)
                HeapFree(GetProcessHeap(), 0, ctx->buf);
            HeapFree(GetProcessHeap(), 0, ctx);
        }
        return S_OK;
    }
    case DPN_MSGID_RECEIVE:
    {
        PDPNMSG_RECEIVE p = (PDPNMSG_RECEIVE)pMsgBuffer;
        if (p && p->pReceiveData && p->dwReceiveDataSize > 0)
        {
            const BYTE* b = (const BYTE*)p->pReceiveData;
            DWORD n = p->dwReceiveDataSize;
            q_push(DPN_MSGID_RECEIVE, (uint32_t)p->dpnidSender, b, n);

            // Log a prefix in hex.
            char hexp[256] = {0};
            DWORD take = n < 64 ? n : 64;
            char* w = hexp;
            for (DWORD i = 0; i < take; i++)
            {
                int m = snprintf(w, sizeof(hexp) - (size_t)(w - hexp), "%02X ", b[i]);
                if (m <= 0) break;
                w += m;
            }
            logline("RECEIVE from=0x%08lx bytes=%lu head=%s\n",
                    (unsigned long)p->dpnidSender, (unsigned long)n, hexp);

            // If it looks like text, log the full string (bounded).
            if (looks_like_text(b, n))
            {
                DWORD max = n < 1023 ? n : 1023;
                char s[1024];
                ZeroMemory(s, sizeof(s));
                memcpy(s, b, max);
                // Replace embedded NULs with '.' for log readability.
                for (DWORD i = 0; i < max; i++)
                {
                    if (s[i] == '\0')
                        s[i] = '.';
                }
                logline("RECV_TEXT: %s\n", s);

                // Auto-replies moved to Go; keep this shim as a transport/event adapter.
                (void)kAutoReplyOnConnectXml;
            }
        }
        // IMPORTANT: Return the receive buffer to DirectPlay. NetPipe delays this until the app
        // is done with the data, but since we only inspect/log and drop it, return immediately.
        if (p && g_dpServer && p->hBufferHandle)
        {
            g_dpServer->ReturnBuffer(p->hBufferHandle, 0);
        }
        return S_OK;
    }
    default:
        // Ignore everything else for now.
        return S_OK;
    }
}

static void SafeRelease(IUnknown** pp)
{
    if (pp && *pp)
    {
        (*pp)->Release();
        *pp = NULL;
    }
}

int32_t DP8_StartServer(uint16_t port)
{
    if (g_dpServer)
    {
        return (int32_t)S_OK;
    }

    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (SUCCEEDED(hr))
    {
        g_comInit = true;
    }
    else if (hr == RPC_E_CHANGED_MODE)
    {
        // Someone already initialized COM differently. We'll continue and hope for the best.
        hr = S_OK;
    }
    else if (hr == S_FALSE)
    {
        g_comInit = true;
        hr = S_OK;
    }
    if (FAILED(hr))
    {
        logline("DP8_StartServer CoInitializeEx failed hr=0x%08lx\n", hr);
        return (int32_t)hr;
    }

    hr = CoCreateInstance(CLSID_DirectPlay8Server, NULL, CLSCTX_INPROC_SERVER,
                          IID_IDirectPlay8Server, (LPVOID*)&g_dpServer);
    if (FAILED(hr))
    {
        logline("DP8_StartServer CoCreateInstance(DirectPlay8Server) failed hr=0x%08lx\n", hr);
        return (int32_t)hr;
    }

    hr = g_dpServer->Initialize(NULL, DP8ServerMsgHandler, 0);
    if (FAILED(hr))
    {
        logline("DP8_StartServer IDirectPlay8Server::Initialize failed hr=0x%08lx\n", hr);
        SafeRelease((IUnknown**)&g_dpServer);
        return (int32_t)hr;
    }

    // Set a server name (some clients query it on connect complete).
    {
        DPN_PLAYER_INFO info;
        ZeroMemory(&info, sizeof(info));
        info.dwSize = sizeof(info);
        info.dwInfoFlags = DPNINFO_NAME;
        info.pwszName = (WCHAR*)L"CompatServer";
        (void)g_dpServer->SetServerInfo(&info, NULL, NULL, DPNOP_SYNC);
    }

    // Create and configure device address (TCP/IP SP + port).
    hr = CoCreateInstance(CLSID_DirectPlay8Address, NULL, CLSCTX_INPROC_SERVER,
                          IID_IDirectPlay8Address, (LPVOID*)&g_deviceAddr);
    if (FAILED(hr))
    {
        logline("DP8_StartServer CoCreateInstance(DirectPlay8Address) failed hr=0x%08lx\n", hr);
        DP8_StopServer();
        return (int32_t)hr;
    }
    hr = g_deviceAddr->SetSP(&CLSID_DP8SP_TCPIP);
    if (FAILED(hr))
    {
        logline("DP8_StartServer IDirectPlay8Address::SetSP(TCPIP) failed hr=0x%08lx\n", hr);
        DP8_StopServer();
        return (int32_t)hr;
    }

    DWORD dwPort = (DWORD)port;
    hr = g_deviceAddr->AddComponent(DPNA_KEY_PORT, &dwPort, sizeof(dwPort), DPNA_DATATYPE_DWORD);
    if (FAILED(hr))
    {
        logline("DP8_StartServer IDirectPlay8Address::AddComponent(PORT=%lu) failed hr=0x%08lx\n",
                (unsigned long)dwPort, hr);
        DP8_StopServer();
        return (int32_t)hr;
    }

    DPN_APPLICATION_DESC desc;
    ZeroMemory(&desc, sizeof(desc));
    desc.dwSize = sizeof(desc);
    // Server must host a client/server session.
    desc.dwFlags = DPNSESSION_CLIENT_SERVER;
    desc.guidApplication = kAppGuid;
    // Many runtimes are picky about GUID_NULL here; generate an instance GUID.
    (void)CoCreateGuid(&desc.guidInstance);
    desc.pwszSessionName = (WCHAR*)L"CompatServer";

    // Host. We do not supply player context data here.
    hr = g_dpServer->Host(&desc, &g_deviceAddr, 1, NULL, NULL, NULL, 0);
    if (FAILED(hr))
    {
        logline("DP8_StartServer Host failed hr=0x%08lx\n", hr);
        DP8_StopServer();
        return (int32_t)hr;
    }

    logline("DP8_StartServer ok port=%u appGuid=77E2D9C2-504E-459F-8416-0848130BBE1E flags=CLIENT_SERVER\n", (unsigned)port);
    return (int32_t)S_OK;
}

void DP8_StopServer(void)
{
    if (g_dpServer)
    {
        g_dpServer->Close(0);
    }
    SafeRelease((IUnknown**)&g_deviceAddr);
    SafeRelease((IUnknown**)&g_dpServer);

    if (g_comInit)
    {
        CoUninitialize();
        g_comInit = false;
    }

    if (kEnableFileLog)
    {
        if (g_logCsInit)
            EnterCriticalSection(&g_logCs);
        if (g_log)
        {
            fclose(g_log);
            g_log = NULL;
        }
        if (g_logCsInit)
        {
            LeaveCriticalSection(&g_logCs);
            DeleteCriticalSection(&g_logCs);
            g_logCsInit = false;
        }
    }

    if (g_qCsInit)
    {
        DeleteCriticalSection(&g_qCs);
        g_qCsInit = false;
    }
}

int32_t DP8_PopEvent(DP8Event* outEvt, uint8_t* outBuf, uint32_t outCap)
{
    if (!outEvt)
        return -1;
    q_init_if_needed();

    EnterCriticalSection(&g_qCs);
    if (g_qLen == 0)
    {
        LeaveCriticalSection(&g_qCs);
        ZeroMemory(outEvt, sizeof(*outEvt));
        return 0;
    }
    QItem* it = &g_q[g_qHead];
    *outEvt = it->evt;

    uint32_t copied = 0;
    if (outBuf && outCap > 0 && it->used > 0)
    {
        copied = it->used;
        if (copied > outCap)
        {
            copied = outCap;
            outEvt->flags |= 1; // truncated-to-caller-buffer
        }
        memcpy(outBuf, it->data, copied);
        outEvt->data_len = copied;
    }
    else
    {
        outEvt->data_len = 0;
    }

    // Pop.
    ZeroMemory(it, sizeof(*it));
    g_qHead = (g_qHead + 1) % kQCap;
    g_qLen--;
    LeaveCriticalSection(&g_qCs);
    return 1;
}

uint32_t DP8_GetQueueDepth(void)
{
    if (!g_qCsInit)
        return 0;
    EnterCriticalSection(&g_qCs);
    uint32_t n = g_qLen;
    LeaveCriticalSection(&g_qCs);
    return n;
}

int32_t DP8_SendTo(uint32_t dpnid, const uint8_t* buf, uint32_t len, uint32_t flags)
{
    if (!g_dpServer)
        return (int32_t)DPNERR_UNINITIALIZED;
    if (!buf || len == 0)
        return (int32_t)DPNERR_INVALIDPARAM;

    // Copy the payload into heap memory so async sends are safe from caller buffer lifetime.
    SendCtx* ctx = (SendCtx*)HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, sizeof(SendCtx));
    if (!ctx)
        return (int32_t)E_OUTOFMEMORY;
    ctx->buf = (BYTE*)HeapAlloc(GetProcessHeap(), 0, len);
    if (!ctx->buf)
    {
        HeapFree(GetProcessHeap(), 0, ctx);
        return (int32_t)E_OUTOFMEMORY;
    }
    memcpy(ctx->buf, buf, len);
    ctx->len = len;

    DPN_BUFFER_DESC bd;
    bd.pBufferData = ctx->buf;
    bd.dwBufferSize = len;

    // For async sends, DirectPlay expects a non-NULL async handle out-param.
    // For SYNC sends, it must be NULL. (Empirical: passing NULL for async can yield E_INVALIDARG.)
    DPNHANDLE hAsync = 0;
    DPNHANDLE* phAsync = (flags & DPNSEND_SYNC) ? NULL : &hAsync;

    HRESULT hr = g_dpServer->SendTo(
        (DPNID)dpnid,
        &bd,
        1,
        0,
        ctx,
        phAsync,
        flags);
    if (hr != DPNERR_PENDING && FAILED(hr))
    {
        // Send did not take ownership of context; free now.
        if (ctx->buf)
            HeapFree(GetProcessHeap(), 0, ctx->buf);
        HeapFree(GetProcessHeap(), 0, ctx);
    }
    return (int32_t)hr;
}
