package exceptions

// debugTemplateHTML is the template for development error pages.
const debugTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.StatusCode}} - {{.StatusText}}</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: #1a1a2e;
            color: #eee;
            line-height: 1.6;
        }
        .header {
            background: linear-gradient(135deg, #e74c3c 0%, #c0392b 100%);
            padding: 2rem;
            color: white;
        }
        .header h1 {
            font-size: 3rem;
            font-weight: 300;
            margin-bottom: 0.5rem;
        }
        .header .message {
            font-size: 1.25rem;
            opacity: 0.9;
            word-break: break-word;
        }
        .header .type {
            font-size: 0.875rem;
            opacity: 0.7;
            margin-top: 0.5rem;
            font-family: 'Monaco', 'Menlo', monospace;
        }
        .meta {
            background: #16213e;
            padding: 1rem 2rem;
            display: flex;
            flex-wrap: wrap;
            gap: 2rem;
            font-size: 0.875rem;
        }
        .meta-item {
            display: flex;
            gap: 0.5rem;
        }
        .meta-item .label {
            color: #888;
        }
        .meta-item .value {
            font-family: 'Monaco', 'Menlo', monospace;
        }
        .container {
            max-width: 1400px;
            margin: 0 auto;
            padding: 2rem;
        }
        .section {
            background: #16213e;
            border-radius: 8px;
            margin-bottom: 1.5rem;
            overflow: hidden;
        }
        .section-header {
            background: #0f3460;
            padding: 1rem 1.5rem;
            font-weight: 600;
            font-size: 0.875rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            color: #94a3b8;
        }
        .section-content {
            padding: 1.5rem;
        }
        .frame {
            margin-bottom: 1rem;
            border: 1px solid #2d3748;
            border-radius: 6px;
            overflow: hidden;
        }
        .frame:last-child {
            margin-bottom: 0;
        }
        .frame-header {
            background: #0f3460;
            padding: 0.75rem 1rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
            cursor: pointer;
        }
        .frame-header:hover {
            background: #1a4a7a;
        }
        .frame-location {
            font-family: 'Monaco', 'Menlo', monospace;
            font-size: 0.8125rem;
        }
        .frame-file {
            color: #60a5fa;
        }
        .frame-line {
            color: #f59e0b;
        }
        .frame-function {
            color: #a78bfa;
            font-size: 0.75rem;
        }
        .frame-source {
            background: #0d1117;
            overflow-x: auto;
        }
        .source-line {
            display: flex;
            font-family: 'Monaco', 'Menlo', monospace;
            font-size: 0.8125rem;
            line-height: 1.5;
        }
        .source-line.highlight {
            background: rgba(231, 76, 60, 0.3);
        }
        .line-number {
            min-width: 4rem;
            padding: 0.25rem 1rem;
            text-align: right;
            color: #6b7280;
            background: #161b22;
            user-select: none;
            border-right: 1px solid #30363d;
        }
        .line-content {
            padding: 0.25rem 1rem;
            white-space: pre;
            flex: 1;
        }
        .context-table {
            width: 100%;
            border-collapse: collapse;
        }
        .context-table th,
        .context-table td {
            padding: 0.75rem 1rem;
            text-align: left;
            border-bottom: 1px solid #2d3748;
        }
        .context-table th {
            color: #94a3b8;
            font-weight: 500;
            width: 200px;
        }
        .context-table td {
            font-family: 'Monaco', 'Menlo', monospace;
            font-size: 0.875rem;
            word-break: break-all;
        }
        .context-table tr:last-child th,
        .context-table tr:last-child td {
            border-bottom: none;
        }
        .previous-error {
            background: #1f2937;
            padding: 1rem;
            border-radius: 4px;
            font-family: 'Monaco', 'Menlo', monospace;
            font-size: 0.875rem;
            color: #f87171;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>{{.StatusCode}} {{.StatusText}}</h1>
        <div class="message">{{.Message}}</div>
        <div class="type">{{.ExceptionType}}</div>
    </div>

    <div class="meta">
        {{if .Method}}<div class="meta-item"><span class="label">Method:</span><span class="value">{{.Method}}</span></div>{{end}}
        {{if .URL}}<div class="meta-item"><span class="label">URL:</span><span class="value">{{.URL}}</span></div>{{end}}
        {{if .RequestID}}<div class="meta-item"><span class="label">Request ID:</span><span class="value">{{.RequestID}}</span></div>{{end}}
        {{if .TraceID}}<div class="meta-item"><span class="label">Trace ID:</span><span class="value">{{.TraceID}}</span></div>{{end}}
        {{if .Timestamp}}<div class="meta-item"><span class="label">Time:</span><span class="value">{{.Timestamp}}</span></div>{{end}}
    </div>

    <div class="container">
        {{if .Frames}}
        <div class="section">
            <div class="section-header">Stack Trace</div>
            <div class="section-content">
                {{range $i, $frame := .Frames}}
                <div class="frame">
                    <div class="frame-header">
                        <div class="frame-location">
                            <span class="frame-file">{{$frame.ShortFile}}</span>:<span class="frame-line">{{$frame.Line}}</span>
                        </div>
                        <div class="frame-function">{{$frame.Package}}.{{$frame.Function}}</div>
                    </div>
                    {{if $frame.Source}}
                    <div class="frame-source">
                        {{range $frame.Source}}
                        <div class="source-line{{if .Highlight}} highlight{{end}}">
                            <div class="line-number">{{.Number}}</div>
                            <div class="line-content">{{.Content}}</div>
                        </div>
                        {{end}}
                    </div>
                    {{end}}
                </div>
                {{end}}
            </div>
        </div>
        {{end}}

        {{if .Context}}
        <div class="section">
            <div class="section-header">Exception Context</div>
            <div class="section-content">
                <table class="context-table">
                    {{range $key, $value := .Context}}
                    <tr>
                        <th>{{$key}}</th>
                        <td>{{$value}}</td>
                    </tr>
                    {{end}}
                </table>
            </div>
        </div>
        {{end}}

        {{if .Previous}}
        <div class="section">
            <div class="section-header">Previous Exception</div>
            <div class="section-content">
                <div class="previous-error">{{.Previous}}</div>
            </div>
        </div>
        {{end}}
    </div>
</body>
</html>`

// errorTemplateHTML is the template for production error pages.
const errorTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.StatusCode}} - {{.StatusText}}</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: #f5f5f5;
            color: #333;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .container {
            text-align: center;
            padding: 2rem;
            max-width: 600px;
        }
        .status-code {
            font-size: 8rem;
            font-weight: 700;
            color: #e74c3c;
            line-height: 1;
            margin-bottom: 1rem;
        }
        .status-text {
            font-size: 1.5rem;
            color: #666;
            margin-bottom: 1rem;
        }
        .message {
            font-size: 1.125rem;
            color: #888;
            margin-bottom: 2rem;
        }
        .home-link {
            display: inline-block;
            padding: 0.75rem 2rem;
            background: #3498db;
            color: white;
            text-decoration: none;
            border-radius: 4px;
            transition: background 0.2s;
        }
        .home-link:hover {
            background: #2980b9;
        }
        .meta {
            margin-top: 2rem;
            font-size: 0.75rem;
            color: #aaa;
        }
        .meta span {
            margin: 0 0.5rem;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="status-code">{{.StatusCode}}</div>
        <div class="status-text">{{.StatusText}}</div>
        <div class="message">{{.Message}}</div>
        <a href="/" class="home-link">Go Home</a>
        {{if or .RequestID .TraceID}}
        <div class="meta">
            {{if .RequestID}}<span>Request ID: {{.RequestID}}</span>{{end}}
            {{if .TraceID}}<span>Trace ID: {{.TraceID}}</span>{{end}}
        </div>
        {{end}}
    </div>
</body>
</html>`

// notFoundTemplateHTML is the template for 404 pages.
const notFoundTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>404 - Page Not Found</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: #f5f5f5;
            color: #333;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .container {
            text-align: center;
            padding: 2rem;
            max-width: 600px;
        }
        .status-code {
            font-size: 8rem;
            font-weight: 700;
            color: #3498db;
            line-height: 1;
            margin-bottom: 1rem;
        }
        .status-text {
            font-size: 1.5rem;
            color: #666;
            margin-bottom: 1rem;
        }
        .message {
            font-size: 1.125rem;
            color: #888;
            margin-bottom: 2rem;
        }
        .home-link {
            display: inline-block;
            padding: 0.75rem 2rem;
            background: #3498db;
            color: white;
            text-decoration: none;
            border-radius: 4px;
            transition: background 0.2s;
        }
        .home-link:hover {
            background: #2980b9;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="status-code">404</div>
        <div class="status-text">Page Not Found</div>
        <div class="message">The page you're looking for doesn't exist or has been moved.</div>
        <a href="/" class="home-link">Go Home</a>
    </div>
</body>
</html>`

// serverErrorTemplateHTML is the template for 500 pages.
const serverErrorTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>500 - Server Error</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: #f5f5f5;
            color: #333;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .container {
            text-align: center;
            padding: 2rem;
            max-width: 600px;
        }
        .status-code {
            font-size: 8rem;
            font-weight: 700;
            color: #e74c3c;
            line-height: 1;
            margin-bottom: 1rem;
        }
        .status-text {
            font-size: 1.5rem;
            color: #666;
            margin-bottom: 1rem;
        }
        .message {
            font-size: 1.125rem;
            color: #888;
            margin-bottom: 2rem;
        }
        .home-link {
            display: inline-block;
            padding: 0.75rem 2rem;
            background: #3498db;
            color: white;
            text-decoration: none;
            border-radius: 4px;
            transition: background 0.2s;
        }
        .home-link:hover {
            background: #2980b9;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="status-code">500</div>
        <div class="status-text">Server Error</div>
        <div class="message">Something went wrong on our end. Please try again later.</div>
        <a href="/" class="home-link">Go Home</a>
    </div>
</body>
</html>`
