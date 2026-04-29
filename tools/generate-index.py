import yaml
import os
import glob

def generate_html():
    charts = []
    # Find all Chart.yaml files
    chart_paths = glob.glob("helm-charts/*/Chart.yaml")
    for cp in chart_paths:
        with open(cp, "r") as f:
            data = yaml.safe_load(f)
            charts.append({
                "name": data.get("name"),
                "version": data.get("version"),
                "description": data.get("description", "No description provided.")
            })
    
    html_template = """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Jdwlabs Platform Helm Repository</title>
    <style>
        :root {
            --bg: #ffffff; --text: #1e293b; --primary: #0284c7; --card-bg: #f8fafc; --border: #e2e8f0; --code-bg: #1e293b; --code-text: #e2e8f0;
        }
        @media (prefers-color-scheme: dark) {
            :root { --bg: #0f172a; --text: #f1f5f9; --primary: #38bdf8; --card-bg: #1e293b; --border: #334155; --code-bg: #000000; }
        }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background-color: var(--bg); color: var(--text); margin: 0; padding: 2rem; display: flex; justify-content: center; line-height: 1.5; }
        .container { max-width: 800px; width: 100%; }
        h1 { color: var(--primary); margin-bottom: 0.25rem; }
        .setup-box { background: var(--card-bg); padding: 1.5rem; border-radius: 0.75rem; border: 1px solid var(--border); margin: 2rem 0; position: relative; }
        pre { background: var(--code-bg); color: var(--code-text); padding: 1rem; border-radius: 0.5rem; overflow-x: auto; position: relative; }
        .copy-btn { position: absolute; top: 0.5rem; right: 0.5rem; cursor: pointer; background: #475569; border: none; color: white; padding: 0.25rem 0.5rem; border-radius: 0.25rem; font-size: 0.7rem; }
        .chart-grid { display: grid; gap: 1rem; }
        .chart-card { background: var(--card-bg); padding: 1.25rem; border-radius: 0.5rem; border: 1px solid var(--border); }
        .chart-meta { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem; }
        .badge { font-size: 0.75rem; background: var(--primary); color: white; padding: 0.2rem 0.6rem; border-radius: 1rem; font-weight: 600; }
        a { color: var(--primary); text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Jdwlabs Helm Charts</h1>
        <p>Official repository for <a href="https://github.com/jdwlabs/platform">Jdwlabs Platform</a> services.</p>
        <div class="setup-box">
            <h3 style="margin-top: 0;">Repository Setup</h3>
            <pre><button class="copy-btn" onclick="copyCode()">Copy</button><code id="code">helm repo add jdwlabs https://jdwlabs.github.io/platform/
helm repo update</code></pre>
        </div>
        <h3>Available Charts</h3>
        <div class="chart-grid">
            {charts_html}
        </div>
    </div>
    <script>
        function copyCode() {
            const code = document.getElementById('code').innerText;
            navigator.clipboard.writeText(code);
            const btn = document.querySelector('.copy-btn');
            btn.innerText = 'Copied!';
            setTimeout(() => btn.innerText = 'Copy', 2000);
        }
    </script>
</body>
</html>
"""

    
    charts_html = ""
    for c in charts:
        charts_html += f"""
            <div class="chart-card">
                <div class="chart-meta">
                    <strong>{c['name']}</strong>
                    <span class="badge">v{c['version']}</span>
                </div>
                <small>{c['description']}</small>
            </div>
        """
        
    with open("index.html", "w") as f:
        f.write(html_template.format(charts_html=charts_html))

if __name__ == "__main__":
    generate_html()
