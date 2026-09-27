"""Stand-in for the streamer's internal API (:8081) so the broker and
controller can be tested end to end on a CI runner with no webcam. It keeps
mute state so a test can see a mute actually take effect downstream."""
import json, http.server
state={"muted":False}
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self,*a): pass
    def reply(self):
        body=json.dumps({"streaming":True,"muted":state["muted"],"uptime":"1m0s","restarts":0}).encode()
        self.send_response(200); self.send_header("Content-Type","application/json"); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        if self.path=="/mute": state["muted"]=True
        elif self.path=="/unmute": state["muted"]=False
        else: return self.send_error(404)
        self.reply()
    def do_GET(self):
        if self.path in ("/status","/healthz"): return self.reply()
        self.send_error(404)
http.server.HTTPServer(("127.0.0.1",8081),H).serve_forever()
