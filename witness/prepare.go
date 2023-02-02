package witness

import (
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/miha-stopar/mpt/state"
)

// addBranchAndPlaceholder adds to the rows a branch and its placeholder counterpart
// (used when one of the proofs have one branch more than the other).
func addBranchAndPlaceholder(statedb *state.StateDB, addr common.Address, rows *[][]byte, proof1, proof2,
		extNibblesS, extNibblesC [][]byte,
		key, neighbourNode []byte,
		keyIndex, extensionNodeInd int,
		additionalBranch, isAccountProof, nonExistingAccountProof,
		isShorterProofLastLeaf bool, branchC16, branchC1 byte, toBeHashed *[][]byte) (bool, bool, int, byte) {
	len1 := len(proof1)
	len2 := len(proof2)

	numberOfNibbles := 0
	var extRows [][]byte
	isExtension := (len1 == len2 + 2) || (len2 == len1 + 2)
	if !isExtension {
		extRows = prepareEmptyExtensionRows(false, false)
		if branchC16 == 1 {
			branchC16 = 0
			branchC1 = 1
		} else {
			branchC16 = 1
			branchC1 = 0
		}
	} else {
		var numNibbles byte
		var extensionRowS []byte
		var extensionRowC []byte
		if len1 > len2 {
			numNibbles, extensionRowS, extensionRowC =
				prepareExtensionRows(extNibblesS, extensionNodeInd, proof1[len1 - 3], proof1[len1 - 3], false, false)
		} else {
			numNibbles, extensionRowS, extensionRowC =
				prepareExtensionRows(extNibblesC, extensionNodeInd, proof2[len2 - 3], proof2[len2 - 3], false, false)
		}
		numberOfNibbles = int(numNibbles)
		extRows = append(extRows, extensionRowS)
		extRows = append(extRows, extensionRowC)

		// adding extension node for hashing:
		if len1 > len2 {
			addForHashing(proof1[len1-3], toBeHashed)
		} else {
			addForHashing(proof2[len2-3], toBeHashed)
		}

		if numberOfNibbles % 2 == 0 {
			if branchC16 == 1 {
				branchC16 = 0
				branchC1 = 1
			} else {
				branchC16 = 1
				branchC1 = 0
			}
		}
	}

	/*
	For special cases when a new extension node is inserted.

	Imagine you have an extension node at n1 n2 n3 n4 (where each of these is a nibble).
	Let's say this extension node has the following nibbles as the extension: n5 n6 n7.
	So at position n1 n2 n3 n4 n5 n6 n7 there is some branch.
	Now we want to add a leaf at position n1 n2 n3 n4 n5 m1 where m1 != n6.
	The adding algorithm walks through the trie, but it bumps into an extension node where
	it should put this leaf. So a new extension node is added at position n1 n2 n3 n4 which only
	has one nibble: n5. So at n1 n2 n3 n4 n5 we have a branch now. In this brach, at position m we
	have a leaf, while at position n6 we have another extension node with one extension nibble: n7.
	At this position (n7) we have the branch from the original extension node.

	When an extension node is inserted because of the added key, C proof will contain this new
	extension node and the underlying branch. However, S proof will stop at the old extension node. 
	This old extension node is not part of the C proof, but we need to ensure that it is in the C trie.
	We need to take into accout that in the C trie the old extension node has a shortened extension.

	The problem is where to store the old extension node. Note that in the above code the new
	extension node and the underlying branch rows are prepared. For example, when len2 > len1 we
	take extension node from proof2[len2 - 3] and branch from proof2[len2 - 2]. In this case,
	the old extension node in proof1[len1 - 1] has been ignored. For this reason we store it
	in the rows before we add a leaf.
	*/
	var longExtNode []byte
	if len1 > len2 {
		longExtNode = proof2[len2 - 1]
	} else {
		longExtNode = proof1[len1 - 1]
	}

	// Note that isModifiedExtNode happens also when we have a branch instead of shortExtNode
	isModifiedExtNode := !isBranch(longExtNode) && !isShorterProofLastLeaf

	if len1 > len2 {
		bRows, branchToBeHashed := prepareParallelBranches(proof1[len1-2], proof1[len1-2], key[keyIndex + numberOfNibbles], true, branchC16, branchC1, isModifiedExtNode)
		*rows = append(*rows, bRows...)
		addForHashing(branchToBeHashed, toBeHashed)
	} else {
		bRows, branchToBeHashed := prepareParallelBranches(proof2[len2-2], proof2[len2-2], key[keyIndex + numberOfNibbles], false, branchC16, branchC1, isModifiedExtNode)
		*rows = append(*rows, bRows...)
		addForHashing(branchToBeHashed, toBeHashed)
	}
	*rows = append(*rows, extRows...)

	return isModifiedExtNode, isExtension, numberOfNibbles, branchC16
}

// prepareWitness takes two GetProof proofs (before and after single modification) and prepares
// a witness for an MPT circuit. Alongside, it prepares the byte streams that need to be hashed
// and inserted into Keccak lookup table.
func prepareWitness(statedb *state.StateDB, addr common.Address, proof1, proof2, extNibblesS, extNibblesC [][]byte, key []byte, neighbourNode []byte,
		isAccountProof, nonExistingAccountProof, nonExistingStorageProof, isShorterProofLastLeaf bool) ([][]byte, [][]byte, bool) {
	rows := make([][]byte, 0)
	toBeHashed := make([][]byte, 0)

	minLen := len(proof1)
	if len(proof2) < minLen {
		minLen = len(proof2)
	}

	keyIndex := 0
	len1 := len(proof1)
	len2 := len(proof2)

	// When a value in the trie is updated, both proofs are of the same length.
	// Otherwise, when a value is added (not updated) and there is no node which needs to be changed
	// into a branch, one proof has a leaf and one does not have it.
	// The third option is when a value is added and the existing leaf is turned into a branch,
	// in this case we have an additional branch in C proof (when deleting a value causes
	// that a branch with two leaves turns into a leaf, we have an additional branch in S proof).

	additionalBranch := false
	if len1 < len2 && len1 > 0 { // len = 0 when trie trie is empty
		// Check if the last proof element in the shorter proof is a leaf -
		// if it is, then there is an additional branch.
		additionalBranch = !isBranch(proof1[len1 - 1])
	} else if len2 < len1 && len2 > 0 {
		additionalBranch = !isBranch(proof2[len2 - 1])
	}

	upTo := minLen
	if (len1 != len2) && additionalBranch {
		upTo = minLen - 1
	}

	var extensionRowS []byte
	var extensionRowC []byte
	extensionNodeInd := 0

	branchC16 := byte(0); 
	branchC1 := byte(1);
	for i := 0; i < upTo; i++ {
		if !isBranch(proof1[i]) {
			if i != len1 - 1 { // extension node
				var numberOfNibbles byte
				numberOfNibbles, extensionRowS, extensionRowC = prepareExtensionRows(extNibblesS, extensionNodeInd, proof1[i], proof2[i], false, false)
				keyIndex += int(numberOfNibbles)
				extensionNodeInd++
				continue
			}

			if isAccountProof {
				l := len(proof1)
				leafS := proof1[l-1]
				leafC := proof2[l-1]

				leafRows, leafForHashing := prepareAccountLeaf(leafS, leafC, key, nonExistingAccountProof, false)
				rows = append(rows, leafRows...)
				toBeHashed = append(toBeHashed, leafForHashing...)
			} else {
				leafRows, leafForHashing := prepareStorageLeafRows(proof1[i], 2, false) // leaf s
				rows = append(rows, leafRows...)
				toBeHashed = append(toBeHashed, leafForHashing)
				leafRows, leafForHashing = prepareStorageLeafRows(proof2[i], 3, false) // leaf s
				rows = append(rows, leafRows...)	

				toBeHashed = append(toBeHashed, leafForHashing)
			}
		} else {
			switchC16 := true // If not extension node, switchC16 = true.
			if extensionRowS != nil {
				keyLen := getExtensionNodeKeyLen(proof1[i-1])
				if keyLen == 1 {
					switchC16 = false
				} else {
					if proof1[i-1][2] != 0 { // If even, switch16 = true.
						switchC16 = false
					}
				}
			}
			if switchC16 {
				if branchC16 == 1 {
					branchC16 = 0
					branchC1 = 1
				} else {
					branchC16 = 1
					branchC1 = 0
				}
			}

			bRows := prepareTwoBranches(proof1[i], proof2[i], key[keyIndex], branchC16, branchC1, false, false)
			keyIndex += 1

			// extension node rows
			if extensionRowS != nil {
				bRows = append(bRows, extensionRowS)
				bRows = append(bRows, extensionRowC)

				// Set isExtension to 1 in branch init.
				bRows[0][isExtensionPos] = 1

				if len(proof1[i-1]) > 56 { // 56 because there is 1 byte for length
					bRows[0][isSExtLongerThan55Pos] = 1
				}
				if len(proof2[i-1]) > 56 {
					bRows[0][isCExtLongerThan55Pos] = 1
				}

				if len(proof1[i-1]) < 32 {
					bRows[0][isExtNodeSNonHashedPos] = 1
				}
				if len(proof2[i-1]) < 32 {
					bRows[0][isExtNodeCNonHashedPos] = 1
				}

				keyLen := getExtensionNodeKeyLen(proof1[i-1])
				// Set whether key extension nibbles are of even or odd length.
				if keyLen == 1 {
					if branchC16 == 1 {
						bRows[0][isExtShortC16Pos] = 1
					} else {
						bRows[0][isExtShortC1Pos] = 1
					}
				} else {
					if proof1[i-1][2] == 0 {
						if branchC16 == 1 {
							bRows[0][isExtLongEvenC16Pos] = 1
						} else {
							bRows[0][isExtLongEvenC1Pos] = 1
						}
					} else {
						if branchC16 == 1 {
							bRows[0][isExtLongOddC16Pos] = 1
						} else {
							bRows[0][isExtLongOddC1Pos] = 1
						}
					}
				}

				// adding extension nodes for hashing:
				addForHashing(proof1[i-1], &toBeHashed)
				addForHashing(proof2[i-1], &toBeHashed)
			} else {
				extRows := prepareEmptyExtensionRows(false, false)
				bRows = append(bRows, extRows...)
			}

			rows = append(rows, bRows...)
			addForHashing(proof1[i], &toBeHashed)
			addForHashing(proof2[i], &toBeHashed)

			// check the two branches
			if extensionNodeInd == 0 {
				for k := 1; k < 17; k++ {
					if k-1 == int(key[i]) {
						continue
					}
					for j := 0; j < branchNodeRLPLen+32; j++ {
						if bRows[k][j] != bRows[k][branch2start+j] {
							panic("witness not properly generated")
						}
					}
				}
			}

			extensionRowS = nil
			extensionRowC = nil
		}
	}	
	
	if len1 != len2 {
		if additionalBranch {
			isModifiedExtNode, isExtension, numberOfNibbles, branchC16 := addBranchAndPlaceholder(statedb, addr, &rows, proof1, proof2, extNibblesS, extNibblesC, key, neighbourNode,
				keyIndex, extensionNodeInd, additionalBranch,
				isAccountProof, nonExistingAccountProof, isShorterProofLastLeaf, branchC16, branchC1, &toBeHashed)

			if isAccountProof {
				addAccountLeafAfterBranchPlaceholder(&rows, proof1, proof2, neighbourNode, key, nonExistingAccountProof, isModifiedExtNode, isExtension, branchC16, numberOfNibbles, &toBeHashed)
			} else {
				addStorageLeafAfterBranchPlaceholder(&rows, proof1, proof2, neighbourNode, key, nonExistingAccountProof, isModifiedExtNode, isExtension, branchC16, numberOfNibbles, &toBeHashed)
			}

			// When a proof element is a modified extension node (new extension node appears at the position
			// of the existing extension node), additional rows are added (extension node before and after
			// modification).
			if isModifiedExtNode {
				addModifiedExtNode(statedb, addr, &rows, proof1, proof2, extNibblesS, extNibblesC, key, neighbourNode,
					keyIndex, extensionNodeInd, numberOfNibbles, additionalBranch,
					isAccountProof, nonExistingAccountProof, isShorterProofLastLeaf, branchC16, branchC1, &toBeHashed)
			}
		} else {
			addLeafAndPlaceholder(&rows, proof1, proof2, key, nonExistingAccountProof, isAccountProof, &toBeHashed)
		}
	} else {
		// Let's always use C proof for non-existing proof.
		// Account proof has drifted leaf as the last row, storage proof has non-existing-storage row
		// as the last row.
		if isBranch(proof2[len(proof2)-1]) {
			// When non existing proof and only the branches are returned, we add a placeholder leaf.
			// This is to enable the lookup (in account leaf row), most constraints are disabled for these rows.
			if !isAccountProof {
				// We need to prepare placeholder storage leaf rows.
				leaf := make([]byte, rowLen)
				// Just some values to avoid assignement errors:
				leaf[0] = 228
				leaf[1] = 130
				leaf[2] = 51

				leafRows, _ := prepareStorageLeafRows(leaf, 2, false)
				rows = append(rows, leafRows...)
				leafRows, _ = prepareStorageLeafRows(leaf, 3, false)
				rows = append(rows, leafRows...)

				pRows := prepareDriftedLeafPlaceholder(isAccountProof)
				rows = append(rows, pRows...)	

				if nonExistingStorageProof {
					leaf := prepareEmptyNonExistingStorageRow()

					isEven := keyIndex % 2 == 0 
					keyLen := int(math.Floor(float64(64-keyIndex) / float64(2))) + 1
					remainingNibbles := key[keyIndex:]
					leaf[1] = byte(keyLen) + 128
					if isEven {
						leaf[2] = 32
					} else {
						leaf[2] = remainingNibbles[0] + 48
					}

					rows = append(rows, leaf)	
				} else {
					nonExistingStorageRow := prepareEmptyNonExistingStorageRow()
					rows = append(rows, nonExistingStorageRow)	
				}
			} else {
				isEven := keyIndex % 2 == 0 
				keyLen := int(math.Floor(float64(64-keyIndex) / float64(2))) + 1
				remainingNibbles := key[keyIndex:]
				offset := 0
				leaf := make([]byte, rowLen)
				leaf[0] = 248
				leaf[2] = byte(keyLen) + 128
				leaf[3 + keyLen] = 184
				leaf[3 + keyLen + 1 + 1] = 248
				leaf[3 + keyLen + 1 + 1 + 1] = leaf[3 + keyLen + 1] - 2
				if isEven {
					leaf[3] = 32
				} else {
					leaf[3] = remainingNibbles[0] + 48
					offset = 1
				}
				for i := 0; i < keyLen - 1; i++ {
					leaf[4+i] = remainingNibbles[2*i + offset] * 16 + remainingNibbles[2*i + 1 + offset]
				}
				
				leafRows, _ := prepareAccountLeaf(leaf, leaf, key, nonExistingAccountProof, true)
				rows = append(rows, leafRows...)

				pRows := prepareDriftedLeafPlaceholder(isAccountProof)
				rows = append(rows, pRows...)	
			}
		} else {
			pRows := prepareDriftedLeafPlaceholder(isAccountProof)
			rows = append(rows, pRows...)	

			if !isAccountProof {
				if nonExistingStorageProof {
					cKeyRow := rows[len(rows) - 3]
					noLeaf := false
					nonExistingStorageRow := prepareNonExistingStorageRow(cKeyRow, key, noLeaf)
					rows = append(rows, nonExistingStorageRow)	
				} else {
					nonExistingStorageRow := prepareEmptyNonExistingStorageRow()
					rows = append(rows, nonExistingStorageRow)	
				}
			}
		}
	}

	return rows, toBeHashed, extensionNodeInd > 0
}