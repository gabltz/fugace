# IRC observer: joins #general, talks to the client under test, records
# everything, and stops when the client under test quits (or after secs).
import socket, sys, time
host, port, peer, newnick, secs = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], float(sys.argv[5])
s = socket.create_connection((host, port)); s.settimeout(0.5)
def send(l): s.sendall((l + "\r\n").encode())
send("NICK observer"); send("USER o 0 * :o"); send("JOIN #general")
buf, lines, t0, sent, done = b"", [], time.time(), set(), False
while time.time() - t0 < secs and not done:
    try:
        d = s.recv(4096)
        if not d: break
        buf += d
    except socket.timeout:
        pass
    while b"\r\n" in buf:
        l, buf = buf.split(b"\r\n", 1); l = l.decode("utf-8", "replace"); lines.append(l)
        if l.startswith("PING"): send("PONG" + l[4:])
        if f":{newnick}!" in l and " QUIT " in l: done = True
        if f":{peer}!" in l and " JOIN " in l and "hi" not in sent:
            sent.add("hi"); send(f"PRIVMSG #general :hello {peer} from observer"); send(f"PRIVMSG {peer} :private to {peer}")
        if f":{newnick}!" in l and "PRIVMSG observer" in l and "dm" not in sent:
            sent.add("dm"); send(f"PRIVMSG {newnick} :reply to new nick")
send("QUIT :done")
for l in lines: print(l)
