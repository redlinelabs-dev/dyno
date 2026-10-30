//! dyno bench server: Zig, std.http.Server on std.Io (Threaded), std only.
const std = @import("std");
const Io = std.Io;
const http = std.http;

const payload_size = 1 << 20; // 1 MiB of random bytes, generated once
const write_chunk = 256 << 10; // bytes handed to the writer per call
const max_download: u64 = 1 << 34;
const index_html = @embedFile("index.html");

var payload: [payload_size]u8 = undefined;

pub fn main(init: std.process.Init) !void {
    const io = init.io;
    io.random(&payload);

    const port_str = init.environ_map.get("PORT") orelse "8080";
    const port = try std.fmt.parseInt(u16, port_str, 10);
    const address = try Io.net.IpAddress.parse("127.0.0.1", port);
    var listener = try address.listen(io, .{ .reuse_address = true });

    var group: Io.Group = .init;
    defer group.cancel(io);
    while (true) {
        const stream = listener.accept(io) catch continue;
        // `group.async` would run the handler inline (blocking accept) once
        // cpu_count-1 connections are busy; `concurrent` gives one thread each.
        group.concurrent(io, handleConnection, .{ io, stream }) catch {
            stream.close(io);
        };
    }
}

fn handleConnection(io: Io, stream: Io.net.Stream) void {
    defer stream.close(io);
    var recv_buffer: [64 * 1024]u8 = undefined;
    var send_buffer: [16 * 1024]u8 = undefined;
    var conn_reader = stream.reader(io, &recv_buffer);
    var conn_writer = stream.writer(io, &send_buffer);
    var server = http.Server.init(&conn_reader.interface, &conn_writer.interface);
    while (server.reader.state == .ready) {
        var request = server.receiveHead() catch return;
        serve(&request) catch return;
    }
}

fn serve(request: *http.Server.Request) !void {
    const target = request.head.target;
    const q = std.mem.findScalar(u8, target, '?');
    const path = if (q) |i| target[0..i] else target;
    const query = if (q) |i| target[i + 1 ..] else "";
    const method = request.head.method;

    if (method == .GET and std.mem.eql(u8, path, "/dl")) {
        const n = parseBytes(query) orelse return badRequest(request);
        if (n > max_download) return badRequest(request);
        var send_buf: [0]u8 = undefined;
        var body = try request.respondStreaming(&send_buf, .{
            .content_length = n,
            .respond_options = .{ .extra_headers = &.{
                .{ .name = "content-type", .value = "application/octet-stream" },
                .{ .name = "cache-control", .value = "no-store" },
            } },
        });
        var left = n;
        var off: usize = 0;
        while (left > 0) {
            const c: usize = @intCast(@min(write_chunk, payload_size - off, left));
            try body.writer.writeAll(payload[off .. off + c]);
            left -= c;
            off = (off + c) % payload_size;
        }
        try body.end();
    } else if (method == .POST and std.mem.eql(u8, path, "/ul")) {
        var body_buf: [0]u8 = undefined;
        const r = try request.readerExpectContinue(&body_buf);
        const total = try r.discardRemaining();
        var num: [24]u8 = undefined;
        const s = try std.fmt.bufPrint(&num, "{d}", .{total});
        try request.respond(s, .{ .extra_headers = &.{.{ .name = "content-type", .value = "text/plain" }} });
    } else if (method == .GET and std.mem.eql(u8, path, "/ping")) {
        try request.respond("", .{ .status = .no_content, .extra_headers = &.{.{ .name = "cache-control", .value = "no-store" }} });
    } else if (method == .GET and std.mem.eql(u8, path, "/")) {
        try request.respond(index_html, .{ .extra_headers = &.{.{ .name = "content-type", .value = "text/html; charset=utf-8" }} });
    } else {
        try request.respond("not found\n", .{ .status = .not_found });
    }
}

fn badRequest(request: *http.Server.Request) !void {
    try request.respond("bad bytes\n", .{ .status = .bad_request });
}

fn parseBytes(query: []const u8) ?u64 {
    var it = std.mem.splitScalar(u8, query, '&');
    while (it.next()) |kv| {
        if (std.mem.startsWith(u8, kv, "bytes="))
            return std.fmt.parseInt(u64, kv["bytes=".len..], 10) catch null;
    }
    return null;
}
