import socket
import threading
import struct
import time

HOST = "127.0.0.1"
PORT = 8888
FRAME_PORT = 8889
HDR = struct.Struct("!IIB")
MAX_FRAME = 4096
RATE = 1048576

CACHE = {}
LOCK = threading.Lock()

STATUS_MESSAGES = {
    501: "Not Implemented",
    502: "Bad Gateway"
}

def url_parse(url, header):
    if not url.lower().startswith("http://"):
        url = "http://" + header.get("host", "") + url

    authority, _, dump = url[7:].partition("/")
    host, _, port = authority.partition(":")
    
    return host.lower(), int(port or 80), "/" + dump

def fetch(host, port, path, headers):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(10)
        sock.connect((host,port))

        sock.sendall(
            f"GET {path} HTTP/1.1\r\nHost: {host}:{port}\r\n"
            f"User-Agent: {headers.get('user-agent', 'MiniProjectProxy/1.0')}\r\n"
            "Accept-Encoding: identity\r\nConnection: close\r\n\r\n".encode()
        )
        # socket.makefile() usage per Python socket module docs
        # https://docs.python.org/3/library/socket.html#socket.socket.makefile
        return sock.makefile("rb").read()

def error_message(connection, status, detail=""):
    error_detail = STATUS_MESSAGES.get(status)
    connection.sendall(f"HTTP/1.1 {status} {error_detail}\r\nConnection: close\r\n\r\n{detail}".encode())

def fetch_cache(url, host, port, path, headers):
    # mutex to prevent race conditions
    with LOCK:                       
        resp = CACHE.get(url)
    if resp is not None: 
        return resp, "HIT"

    resp = fetch(host, port, path, headers)
    if not resp: 
            raise OSError("Empty Response")

    with LOCK:
        CACHE[url] = resp

    return resp, "MISS"

def retrieve_object(urlTarget, headers):
    # parsing URL and partitioning contents
    host, port, path = url_parse(urlTarget, headers)
    url = f"http://{host}:{port}{path}"
    resp, state = fetch_cache(url, host, port, path, headers)
    status, _, dump = resp.partition(b"\r\n")
    return status + f"\r\nX-Cache: {state}\r\n".encode() + dump, state, url

def handle(connection, addr):
    try:
        # timeout loop
        connection.settimeout(5)
        req = b""
        while b"\r\n\r\n" not in req:
            if not (chunk := connection.recv(4096)):
                return
            req += chunk

        # request partitioning
        lines = req.partition(b"\r\n\r\n")[0].decode(errors="replace").split("\r\n")
        method, target = (lines[0].split(" ") + ["", ""])[:2]
        headers = {k.lower().strip(): v.strip()
                   for k, s, v in (l.partition(":") for l in lines[1:]) if s}

        # error handling for everything outside of plain HTTP
        if method != "GET" or target.lower().startswith("https://"):
            return error_message(connection, 501)

        # retrieving objects
        try:
            obj, state, url = retrieve_object(target, headers)

        except OSError as error:
            return error_message(connection, 502, error)

        # shutdown / clean
        print(f"[{addr[0]}] {state} {url}")
        connection.sendall(obj)

    except Exception as excpt:
        print(f"Error handling {addr}: {excpt}")

    finally:
        connection.close()

# HOL blocking resolution with frames
def frame(connection, addr):
    unsent = {}
    remaining = 1

    cv = threading.Condition()

    def framing_thread(sid, urlTarget):
        # fetching objects
        nonlocal remaining
        try:
            obj, state, url = retrieve_object(urlTarget, {})
            print(f"[{addr[0]}] stream {sid} {state} {url}")

        except OSError as e:
            # error handling
            obj = f"HTTP/1.1 502 Bad Gateway\r\n\r\n{e}".encode()

        with cv:
            # cutting out objects with frame size
            unsent[sid] = [obj[i:i+MAX_FRAME] for i in range(0, len(obj), MAX_FRAME)]
            remaining -= 1
            cv.notify()

    def owner():
        # controller for objects in pipeline
        while True:
            with cv:
                while not unsent and remaining:
                    cv.wait()
                if not unsent:
                    return

                # round robin scheduling algorithm
                sid, payload_pipe = next(iter(unsent.items()))
                payload = payload_pipe.pop(0)
                last = not payload_pipe
                del unsent[sid]

                if not last:
                    unsent[sid] = payload_pipe

            connection.sendall(HDR.pack(len(payload), sid, last)+ payload)

            if RATE:
                time.sleep(len(payload) / RATE)

    writeThread = threading.Thread(target=owner, daemon=True)
    writeThread.start()

    try:
        # reading the socket in a proper format
        reader = connection.makefile("rb")
        while head := reader.read(HDR.size):
            n, sid, _ = HDR.unpack(head)
            urlTarget = reader.read(n).decode(errors="replace")
            with cv:
                remaining+=1
            threading.Thread(target=framing_thread, args=(sid, urlTarget), daemon=True).start()

    finally:
        with cv:
            remaining -= 1
            cv.notify()

        writeThread.join(timeout=30)
        connection.close()

# To demonstrate framing tech
def framed_app():
    with socket.create_server((HOST, FRAME_PORT), backlog=50) as server:
        print(f"Framed proxy on {HOST}:{FRAME_PORT} rate={RATE}")
        while True:
            threading.Thread(target=frame, args=server.accept(), daemon=True).start()

def main():

    threading.Thread(target=framed_app, daemon=True).start()

    with socket.create_server((HOST, PORT), backlog=50) as server:
        print(f"Proxy listening on http://{HOST}:{PORT}")
        try:
            while True:
                threading.Thread(target=handle, args=server.accept(), daemon=True).start()
        except KeyboardInterrupt:
            print("Interrupted.")


if __name__ == "__main__":
    main()
