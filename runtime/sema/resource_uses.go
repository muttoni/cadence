/*
 * Cadence - The resource-oriented smart contract programming language
 *
 * Copyright 2019-2020 Dapper Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package sema

import (
	"github.com/onflow/cadence/runtime/ast"
)

type ResourceUse struct {
	UseAfterInvalidationReported bool
}

type ResourceUses struct {
	Parent    *ResourceUses
	positions map[ast.Position]ResourceUse
}

// ForEach calls the given function for each resource use in the set.
// It can be used to iterate over all uses.
//
func (rus *ResourceUses) ForEach(cb func(pos ast.Position, use ResourceUse) error) error {

	resourceUses := rus

	for resourceUses != nil {

		for pos, use := range resourceUses.positions {
			err := cb(pos, use)
			if err != nil {
				return err
			}
		}

		resourceUses = resourceUses.Parent
	}

	return nil
}

func (rus ResourceUses) Contains(pos ast.Position) bool {
	if rus.positions != nil {
		_, ok := rus.positions[pos]
		if ok {
			return true
		}
	}

	if rus.Parent != nil {
		return rus.Parent.Contains(pos)
	}

	return false
}

func (rus ResourceUses) getOrEmpty(pos ast.Position) ResourceUse {
	if rus.positions != nil {
		use, ok := rus.positions[pos]
		if ok {
			return use
		}
	}

	if rus.Parent != nil {
		return rus.Parent.getOrEmpty(pos)
	}

	return ResourceUse{}
}

// Add adds the given resource use to this set.
//
func (rus *ResourceUses) Add(pos ast.Position) {
	if rus.Contains(pos) {
		return
	}
	if rus.positions == nil {
		rus.positions = map[ast.Position]ResourceUse{}
	}
	rus.positions[pos] = ResourceUse{}
}

// MarkUseAfterInvalidationReported marks the use after invalidation
// of the resource at the given position as reported.
//
func (rus *ResourceUses) MarkUseAfterInvalidationReported(pos ast.Position) {
	use := rus.getOrEmpty(pos)
	use.UseAfterInvalidationReported = true
	if rus.positions == nil {
		rus.positions = map[ast.Position]ResourceUse{}
	}
	rus.positions[pos] = use
}

// IsUseAfterInvalidationReported returns true if the use after invalidation
// of the resource at the given position is reported.
//
func (rus ResourceUses) IsUseAfterInvalidationReported(pos ast.Position) bool {
	return rus.getOrEmpty(pos).UseAfterInvalidationReported
}

// Merge adds the resource uses of the given set to this set.
//
func (rus *ResourceUses) Merge(other ResourceUses) {
	if rus.positions == nil {
		rus.positions = map[ast.Position]ResourceUse{}
	}

	_ = other.ForEach(func(pos ast.Position, use ResourceUse) error {
		if !use.UseAfterInvalidationReported {
			use.UseAfterInvalidationReported = rus.getOrEmpty(pos).UseAfterInvalidationReported
		}

		rus.positions[pos] = use

		// NOTE: when changing this function to return an error,
		// also return it from the outer function,
		// as the outer error is currently ignored!
		return nil
	})
}

// Size returns the number of resource uses in this set.
//
func (rus ResourceUses) Size() int {
	return len(rus.positions)
}

// Clone returns a new child resource use set that contains all entries of this parent set.
// Changes to the returned set will only be applied in this set, not the parent.
//
func (rus *ResourceUses) Clone() *ResourceUses {
	return &ResourceUses{
		Parent: rus,
	}
}
