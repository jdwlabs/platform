import yaml
import sys
import glob
import os

def validate_tenant_file(file_path):
    print(f"Validating {file_path}")
    try:
        with open(file_path) as fp:
            data = yaml.safe_load(fp)
    except Exception as e:
        print(f"Error reading {file_path}: {e}")
        return False

    try:
        assert 'name' in data, f'{file_path} missing name key'
        assert 'namespaces' in data, f'{file_path} missing namespaces key'
        assert 'services' in data, f'{file_path} missing services key'
        
        required = {'name', 'repo', 'revision', 'namespace', 'postInstall', 'syncWave'}
        for svc in data['services']:
            missing = required - set(svc.keys())
            assert not missing, f'Service {svc.get("name")} missing fields: {missing}'
            assert "chart" in svc or "chartPath" in svc, f'Service {svc.get("name")} must have either "chart" or "chartPath"'
        
        print(f"  {file_path} valid: {len(data['services'])} services")
        return True
    except AssertionError as e:
        print(f"  Validation failed for {file_path}: {e}")
        return False

def main():
    # Find all tenant.yaml files in tenants/*/tenant.yaml
    base_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    pattern = os.path.join(base_dir, "tenants", "*", "tenant.yaml")
    files = glob.glob(pattern)
    
    if not files:
        print("No tenant.yaml files found.")
        return 0
    
    success = True
    for f in files:
        if not validate_tenant_file(f):
            success = False
            
    if not success:
        sys.exit(1)
    
    print("All tenant files validated successfully.")

if __name__ == "__main__":
    main()
