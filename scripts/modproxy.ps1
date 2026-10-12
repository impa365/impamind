# Local Go module proxy: serves files from a cache directory and fills cache
# misses from proxy.golang.org. Run on the host; containers use it as GOPROXY.
param([int]$Port = 8099, [string]$Cache = "$env:LOCALAPPDATA\impamind-modcache")
$ErrorActionPreference = 'Stop'
Add-Type -ReferencedAssemblies System.Net.Http -TypeDefinition @'
using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Threading;

public static class ModProxy
{
    static readonly object Gate = new object();
    static readonly HttpClient Http = new HttpClient();

    public static void Run(int port, string cache)
    {
        Directory.CreateDirectory(cache);
        var listener = new HttpListener();
        listener.Prefixes.Add("http://127.0.0.1:" + port + "/");
        listener.Start();
        Console.WriteLine("modproxy on :" + port + " cache=" + cache);
        while (listener.IsListening)
        {
            var ctx = listener.GetContext();
            ThreadPool.QueueUserWorkItem(delegate { Serve(ctx, cache); });
        }
    }

    static void Serve(HttpListenerContext ctx, string cache)
    {
        var rel = Uri.UnescapeDataString(ctx.Request.Url.AbsolutePath.TrimStart('/'));
        try
        {
            var dest = Path.Combine(cache, rel.Replace('/', Path.DirectorySeparatorChar));
            lock (Gate)
            {
                if (!File.Exists(dest))
                {
                    Directory.CreateDirectory(Path.GetDirectoryName(dest));
                    var tmp = dest + ".partial";
                    using (var res = Http.GetAsync("https://proxy.golang.org/" + rel).Result)
                    {
                        if (!res.IsSuccessStatusCode) throw new Exception("upstream " + (int)res.StatusCode + " for " + rel);
                        using (var fs = File.Create(tmp)) res.Content.CopyToAsync(fs).Wait();
                    }
                    if (dest.EndsWith(".zip") && new FileInfo(tmp).Length < 64) throw new Exception("truncated zip for " + rel);
                    File.Move(tmp, dest);
                }
            }
            var bytes = File.ReadAllBytes(dest);
            ctx.Response.StatusCode = 200;
            ctx.Response.ContentType = "application/octet-stream";
            ctx.Response.ContentLength64 = bytes.Length;
            ctx.Response.OutputStream.Write(bytes, 0, bytes.Length);
            Console.WriteLine("200 " + rel);
        }
        catch (Exception ex)
        {
            try
            {
                ctx.Response.StatusCode = 502;
                var msg = System.Text.Encoding.UTF8.GetBytes(ex.Message);
                ctx.Response.OutputStream.Write(msg, 0, msg.Length);
            }
            catch {}
            Console.WriteLine("502 " + rel + " " + ex.Message);
        }
        finally { ctx.Response.Close(); }
    }
}
'@
[ModProxy]::Run($Port, (Resolve-Path (New-Item -ItemType Directory -Force -Path $Cache)).Path)
