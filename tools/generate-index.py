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
            --bg: #f8fafc; --text: #1e293b; --primary: #0284c7; --card-bg: #ffffff; --border: #e2e8f0; --code-bg: #1e293b; --code-text: #e2e8f0;
        }
        @media (prefers-color-scheme: dark) {
            :root { --bg: #0f172a; --text: #f1f5f9; --primary: #38bdf8; --card-bg: #1e293b; --border: #334155; --code-bg: #000000; }
        }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background-color: var(--bg); color: var(--text); margin: 0; padding: 3rem 2rem; display: flex; justify-content: center; line-height: 1.6; }
        .container { max-width: 900px; width: 100%; }
        header { margin-bottom: 3rem; }
        h1 { color: var(--primary); font-size: 2.5rem; margin-bottom: 0.5rem; }
        .setup-box { background: var(--card-bg); padding: 2rem; border-radius: 1rem; border: 1px solid var(--border); box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1); }
        pre { background: var(--code-bg); color: var(--code-text); padding: 1.5rem; border-radius: 0.75rem; overflow-x: auto; position: relative; font-size: 0.95rem; }
        .copy-btn { position: absolute; top: 0.75rem; right: 0.75rem; cursor: pointer; background: #475569; border: none; color: white; padding: 0.4rem 0.8rem; border-radius: 0.5rem; font-size: 0.75rem; font-weight: 600; }
        .section-title { font-size: 1.5rem; margin: 2rem 0 1rem; display: flex; align-items: center; gap: 0.5rem; }
        .chart-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 1.5rem; }
        .chart-card { background: var(--card-bg); padding: 1.5rem; border-radius: 1rem; border: 1px solid var(--border); display: flex; flex-direction: column; transition: transform 0.2s; box-shadow: 0 1px 3px rgb(0 0 0 / 0.1); }
        .chart-card:hover { transform: translateY(-4px); border-color: var(--primary); }
        .chart-header { display: flex; justify-content: space-between; align-items: start; margin-bottom: 1rem; }
        .chart-name { font-size: 1.25rem; font-weight: 700; color: var(--primary); }
        .badge { font-size: 0.75rem; background: var(--border); padding: 0.25rem 0.75rem; border-radius: 999px; font-weight: 600; }
        footer { margin-top: 4rem; text-align: center; color: #64748b; font-size: 0.875rem; border-top: 1px solid var(--border); padding-top: 2rem; }
        a { color: var(--primary); text-decoration: none; font-weight: 500; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>Jdwlabs Platform Helm Charts</h1>
            <p>Official Helm repository for the Jdwlabs Platform infrastructure services.</p>
        </header>

        <div class="setup-box">
            <h2 style="margin-top: 0;">Repository Setup</h2>
            <pre><button class="copy-btn" onclick="copyCode()">Copy</button><code id="code">helm repo add jdwlabs https://jdwlabs.github.io/platform/
helm repo update</code></pre>
        </div>

        <h2 class="section-title">📦 Available Charts</h2>
        <div class="chart-grid">
            {charts_html}
        </div>

        <footer>
            &copy; 2026 Jdwlabs &bull; <a href="https://github.com/jdwlabs/platform">View Source on GitHub</a>
        </footer>
    </div>
    <script>
        function copyCode() {{
            const code = document.getElementById('code').innerText;
            navigator.clipboard.writeText(code);
            const btn = document.querySelector('.copy-btn');
            btn.innerText = 'Copied!';
            setTimeout(() => btn.innerText = 'Copy', 2000);
        }}
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
