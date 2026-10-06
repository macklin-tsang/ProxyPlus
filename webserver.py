import socket
import threading
import os
import datetime
import email.utils

# Setup
HOST = "0.0.0.0"
PORT = 8080
SERVER_ROOT = os.path.dirname(os.path.abspath(__file__))
SUPPORTED_VERSIONS = ("HTTP/1.0", "HTTP/1.1")

# Define status messages mapping for building responses easily
STATUS_MESSAGES = {
    200: "OK",
    304: "Not Modified",
    403: "Forbidden",
    404: "Not Found",
    505: "HTTP Version Not Supported",
}


def build_response(status_code, headers=None, body=b""):
    headers = headers or {}
    reason = STATUS_MESSAGES.get(status_code, "Unknown")
    status_line = f"HTTP/1.1 {status_code} {reason}\r\n"

    default_headers = {
        "Server": "MiniProjectServer/1.0",
        "Date": email.utils.formatdate(usegmt=True),
        "Connection": "close",
    }
    if body:
        default_headers["Content-Length"] = str(len(body))
    default_headers.update(headers)

    header_lines = "".join(f"{k}: {v}\r\n" for k, v in default_headers.items())
    response = status_line.encode() + header_lines.encode() + b"\r\n" + body
    return response


def parse_request(raw_request):
    try:
        head, _, _ = raw_request.partition(b"\r\n\r\n")
        lines = head.decode(errors="replace").split("\r\n")
        request_line = lines[0]
        parts = request_line.split(" ")
        if len(parts) != 3:
            return None
        method, path, version = parts

        headers = {}
        for line in lines[1:]:
            if ":" in line:
                key, _, value = line.partition(":")
                headers[key.strip().lower()] = value.strip()

        return method, path, version, headers
    except Exception:
        return None


def resolve_path(url_path):
    # Strip query string / fragment for this simple server
    url_path = url_path.split("?")[0].split("#")[0]
    if url_path == "/":
        url_path = "/test.html"

    # Normalize and join with root to prevent ../../ escaping the root
    candidate = os.path.normpath(os.path.join(SERVER_ROOT, url_path.lstrip("/")))
    if not candidate.startswith(SERVER_ROOT):
        return None  # path traversal attempt, caller should return 403, define later in report.
    return candidate


def handle_connection(conn, addr):
    try:
        conn.settimeout(5)
        raw_request = b""
        # Keep on reading until we have full headers 
        while b"\r\n\r\n" not in raw_request:
            chunk = conn.recv(4096)
            if not chunk:
                break
            raw_request += chunk

        if not raw_request:
            conn.close()
            return

        parsed = parse_request(raw_request)
        if parsed is None:
            # Not in requirements but used as fallback just in case
            conn.sendall(build_response(400, body=b"Bad Request"))
            conn.close()
            return

        method, url_path, version, headers = parsed
        print(f"[{addr}] {method} {url_path} {version}")

        # 505: unsupported HTTP version
        if version not in SUPPORTED_VERSIONS:
            conn.sendall(build_response(505, body=b"HTTP Version Not Supported"))
            conn.close()
            return

        # Only handle get for this project
        if method != "GET":
            conn.sendall(build_response(404, body=b"Not Found"))
            conn.close()
            return

        filepath = resolve_path(url_path)

        # 403: path escapes server root, or file exists but unreadable
        if filepath is None:
            conn.sendall(build_response(403, body=b"Forbidden"))
            conn.close()
            return

        if os.path.exists(filepath) and not os.access(filepath, os.R_OK):
            conn.sendall(build_response(403, body=b"Forbidden"))
            conn.close()
            return

        # 404: file not found
        if not os.path.isfile(filepath):
            conn.sendall(build_response(404, body=b"<h1>404 Not Found</h1>"))
            conn.close()
            return

        mtime = os.path.getmtime(filepath)
        last_modified = email.utils.formatdate(mtime, usegmt=True)

        # 304: client's cached copy is still fresh
        if "if-modified-since" in headers:
            try:
                client_time = email.utils.parsedate_to_datetime(headers["if-modified-since"])
                file_time = datetime.datetime.fromtimestamp(mtime, tz=datetime.timezone.utc)
                if client_time >= file_time:
                    conn.sendall(build_response(304, headers={"Last-Modified": last_modified}))
                    conn.close()
                    return
            except Exception:
                pass  # fall through to base 200 response if header is malformed

        # 200: serve the file
        with open(filepath, "rb") as f:
            body = f.read()

        content_type = "text/html" if filepath.endswith(".html") else "application/octet-stream"
        conn.sendall(build_response(
            200,
            headers={"Content-Type": content_type, "Last-Modified": last_modified},
            body=body,
        ))

    except socket.timeout:
        pass
    except Exception as e:
        print(f"Error handling {addr}: {e}")
    finally:
        conn.close()


def main():
    # Create a TCP socket
    server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    # Helps with testing and restarting server quickly
    server_socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    #Bind and listen with backlog queue up to 50 connections
    server_socket.bind((HOST, PORT))
    server_socket.listen(50)
    print(f"Serving {SERVER_ROOT} on http://{HOST}:{PORT}")

    try:
        while True:
            conn, addr = server_socket.accept()
            t = threading.Thread(target=handle_connection, args=(conn, addr), daemon=True)
            t.start()
    except KeyboardInterrupt:
        print("\nShutting down.")
    finally:
        server_socket.close()


if __name__ == "__main__":
    main()