import socket, sys, time
from proxy import HDR, FRAME_PORT

def send_requests(sock: socket.socket, urls: list[str]):
    # Send each url as a framed request and half close socket so server knows no more input is coming.
    for i, url in enumerate(urls):
        payload = url.encode()
        stream_id = i * 2 + 1
        sock.sendall(HDR.pack(len(payload), stream_id, 0) + payload)
    sock.shutdown(socket.SHUT_WR)


def receive_responses(sock: socket.socket, t0: float):
    #Read framed responses until the end of file. Print totals as each stream finishes.
    received: dict[int, int] = {}
    reader = sock.makefile("rb")
    while head := reader.read(9):
        length, stream_id, end = HDR.unpack(head)
        received[stream_id] = received.get(stream_id, 0) + len(reader.read(length))
        if end:
            elapsed_time = time.time() - t0
            print(f"stream {stream_id}  {received[stream_id]:>9} B  done at {elapsed_time:.3f}s")

def main():
    urls = sys.argv[1:]
    if not urls:
        sys.exit("usage: framed_client.py <url> [url ...]")

    sock = socket.create_connection(("127.0.0.1", FRAME_PORT))
    t0 = time.time()
    send_requests(sock, urls)
    receive_responses(sock, t0)

if __name__ == "__main__":
    main()