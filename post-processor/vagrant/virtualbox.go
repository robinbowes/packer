package vagrant

import (
	"errors"
	"fmt"
	"github.com/mitchellh/mapstructure"
	"github.com/mitchellh/packer/packer"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

type VBoxBoxConfig struct {
	OutputPath          string `mapstructure:"output"`
	VagrantfileTemplate string `mapstructure:"vagrantfile_template"`

	PackerBuildName string `mapstructure:"packer_build_name"`
}

type VBoxVagrantfileTemplate struct {
	BaseMacAddress string
}

type VBoxBoxPostProcessor struct {
	config VBoxBoxConfig
}

func (p *VBoxBoxPostProcessor) Configure(raws ...interface{}) error {
	for _, raw := range raws {
		err := mapstructure.Decode(raw, &p.config)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *VBoxBoxPostProcessor) PostProcess(ui packer.Ui, artifact packer.Artifact) (packer.Artifact, bool, error) {
	var err error
	tplData := &VBoxVagrantfileTemplate{}
	tplData.BaseMacAddress, err = p.findBaseMacAddress(artifact)
	if err != nil {
		return nil, false, err
	}

	// Compile the output path
	outputPath, err := ProcessOutputPath(p.config.OutputPath,
		p.config.PackerBuildName, "virtualbox", artifact)
	if err != nil {
		return nil, false, err
	}

	// Create a temporary directory for us to build the contents of the box in
	dir, err := ioutil.TempDir("", "packer")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)

	// Copy all of the original contents into the temporary directory
	for _, path := range artifact.Files() {
		ui.Message(fmt.Sprintf("Copying: %s", path))

		dstPath := filepath.Join(dir, filepath.Base(path))
		if err := CopyContents(dstPath, path); err != nil {
			return nil, false, err
		}
	}

	// Create the Vagrantfile from the template
	vf, err := os.Create(filepath.Join(dir, "Vagrantfile"))
	if err != nil {
		return nil, false, err
	}
	defer vf.Close()

	vagrantfileContents := defaultVBoxVagrantfile
	if p.config.VagrantfileTemplate != "" {
		f, err := os.Open(p.config.VagrantfileTemplate)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()

		contents, err := ioutil.ReadAll(f)
		if err != nil {
			return nil, false, err
		}

		vagrantfileContents = string(contents)
	}

	t := template.Must(template.New("vagrantfile").Parse(vagrantfileContents))
	t.Execute(vf, tplData)
	vf.Close()

	// Create the metadata
	metadata := map[string]string{"provider": "virtualbox"}
	if err := WriteMetadata(dir, metadata); err != nil {
		return nil, false, err
	}

	// Rename the OVF file to box.ovf, as required by Vagrant
	ui.Message("Renaming the OVF to box.ovf...")
	if err := p.renameOVF(dir); err != nil {
		return nil, false, err
	}

	// Compress the directory to the given output path
	ui.Message(fmt.Sprintf("Compressing box..."))
	if err := DirToBox(outputPath, dir); err != nil {
		return nil, false, err
	}

	return NewArtifact("virtualbox", outputPath), false, nil
}

func (p *VBoxBoxPostProcessor) findBaseMacAddress(a packer.Artifact) (string, error) {
	log.Println("Looking for OVF for base mac address...")
	var ovf string
	for _, f := range a.Files() {
		if strings.HasSuffix(f, ".ovf") {
			log.Printf("OVF found: %s", f)
			ovf = f
			break
		}
	}

	if ovf == "" {
		return "", errors.New("ovf file couldn't be found")
	}

	f, err := os.Open(ovf)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := ioutil.ReadAll(f)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`<Adapter slot="0".+?MACAddress="(.+?)"`)
	matches := re.FindSubmatch(data)
	if matches == nil {
		return "", errors.New("can't find base mac address in OVF")
	}

	log.Printf("Base mac address: %s", string(matches[1]))
	return string(matches[1]), nil
}

func (p *VBoxBoxPostProcessor) renameOVF(dir string) error {
	log.Println("Looking for OVF to rename...")
	matches, err := filepath.Glob(filepath.Join(dir, "*.ovf"))
	if err != nil {
		return err
	}

	if len(matches) > 1 {
		return errors.New("More than one OVF file in VirtualBox artifact.")
	}

	log.Printf("Renaming: '%s' => box.ovf", matches[0])
	return os.Rename(matches[0], filepath.Join(dir, "box.ovf"))
}

var defaultVBoxVagrantfile = `
Vagrant.configure("2") do |config|
config.vm.base_mac = "{{ .BaseMacAddress }}"
end
`
