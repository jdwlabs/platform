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
    <title>Jdwlabs Helm Repository</title>
    <style>
        :root {
            --bg: #ffffff;
            --text: #1e293b;
            --primary: #0284c7;
            --card-bg: #f8fafc;
            --border: #e2e8f0;
        }
        @media (prefers-color-scheme: dark) {
            :root {
                --bg: #0f172a;
                --text: #f8fafc;
                --primary: #38bdf8;
                --card-bg: #1e293b;
                --border: #334155;
            }
        }
        body {
            font-family: system-ui, -apple-system, sans-serif;
            background-color: var(--bg);
            color: var(--text);
            margin: 0;
            padding: 2rem;
            display: flex;
            justify-content: center;
        }
        .container { max-width: 800px; width: 100%; }
        h1 { color: var(--primary); }
        .setup-box {
            background: var(--card-bg);
            padding: 1.5rem;
            border-radius: 0.75rem;
            border: 1px solid var(--border);
            margin-bottom: 2rem;
        }
        code {
            background: rgba(0,0,0,0.1);
            padding: 0.2rem 0.4rem;
            border-radius: 0.25rem;
            font-family: monospace;
        }
        .chart-grid { display: grid; gap: 1rem; }
        .chart-card {
            background: var(--card-bg);
            padding: 1.25rem;
            border-radius: 0.5rem;
            border: 1px solid var(--border);
        }
        .chart-meta { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
        .badge {
            font-size: 0.8rem;
            background: var(--primary);
            color: white;
            padding: 0.2rem 0.6rem;
            border-radius: 1rem;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Jdwlabs Helm Charts</h1>
        <div class="setup-box">
            <h3>Repository Setup</h3>
            <code>helm repo add jdwlabs https://jdwlabs.github.io/platform/</code><br>
            <code>helm repo update</code>
        </div>
        <div class="chart-grid">
            {charts_html}
        </div>
    </div>
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
