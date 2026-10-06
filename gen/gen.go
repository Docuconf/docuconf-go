// Package gen is the original docuconf code generator: it builds a
// configuration struct and Markdown docs from Go builder calls.
//
// Deprecated: declare configuration as a caarlos0/env struct and use
// docuconf.Parse and docuconf.Export (or the docuconf export command)
// instead. This package will be removed in a future release.
package gen

import "os"

// WriteAll writes all the services to the file system.
//
// Deprecated: see the package documentation.
func WriteAll(services []*Service) error {
	for _, service := range services {
		err := service.Write()
		if err != nil {
			return err
		}
	}
	return nil
}

// Writes the generated code to the file system
func (s *Service) Write() error {
	result, err := s.execute()
	if err != nil {
		return err
	}
	for _, file := range result.GoFiles {
		err := os.WriteFile(file.Path, []byte(file.Content), 0644)
		if err != nil {
			return err
		}
	}
	for _, file := range result.MarkDownFiles {
		err := os.WriteFile(file.Path, []byte(file.Content), 0644)
		if err != nil {
			return err
		}
	}
	return nil
}
